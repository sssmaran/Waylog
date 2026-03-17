package ingest

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	planStoreTTL     = 5 * time.Minute
	planStoreMaxSize = 100
	planEventMaxLog  = 21
)

// PlanEvent is a single ordered event emitted during plan execution.
type PlanEvent struct {
	Type string // "step_start" | "step_complete" | "done"
	Data []byte // JSON payload
}

// PlanEntry tracks a single plan's execution state and its SSE subscribers.
type PlanEntry struct {
	ID          string
	Status      string      // "running" | "complete" | "partial" | "failed"
	Result      *PlanResult // set on completion
	Events      []PlanEvent // ordered log, max planEventMaxLog entries
	CreatedAt   time.Time
	CompletedAt *time.Time
	subscribers map[uint64]chan PlanEvent
	nextSubID   uint64
}

// PlanStore is an in-memory store for plan execution progress.
type PlanStore struct {
	mu      sync.Mutex
	entries map[string]*PlanEntry
	order   []string // insertion order for LRU eviction
	closed  bool
}

// NewPlanStore creates an empty PlanStore.
func NewPlanStore() *PlanStore {
	return &PlanStore{
		entries: make(map[string]*PlanEntry),
	}
}

// generatePlanID creates a plan ID by reusing the request ID generator and
// stripping the "req_" prefix.
func generatePlanID() string {
	return strings.TrimPrefix(generateRequestID(), "req_")
}

// Create registers a new running plan and returns its ID.
func (ps *PlanStore) Create() string {
	id := generatePlanID()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return id
	}
	ps.evictLocked()
	ps.entries[id] = &PlanEntry{
		ID:          id,
		Status:      "running",
		CreatedAt:   time.Now(),
		subscribers: make(map[uint64]chan PlanEvent),
	}
	ps.order = append(ps.order, id)
	return id
}

// Get returns the entry for id if it exists and has not expired.
func (ps *PlanStore) Get(id string) (*PlanEntry, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	e, ok := ps.entries[id]
	if !ok {
		return nil, false
	}
	if e.CompletedAt != nil && time.Since(*e.CompletedAt) > planStoreTTL {
		ps.deleteLocked(id)
		return nil, false
	}
	return e, true
}

// Subscribe attaches a subscriber channel to the given plan.
// For running plans, existing events are replayed asynchronously before live events arrive.
// For completed plans, all events are replayed and the channel is closed.
// Returns (ch, subID, ok). ok=false when the plan does not exist or the store is closed.
func (ps *PlanStore) Subscribe(id string) (<-chan PlanEvent, uint64, bool) {
	ps.mu.Lock()
	e, ok := ps.entries[id]
	if !ok || ps.closed {
		ps.mu.Unlock()
		return nil, 0, false
	}
	// TTL check
	if e.CompletedAt != nil && time.Since(*e.CompletedAt) > planStoreTTL {
		ps.deleteLocked(id)
		ps.mu.Unlock()
		return nil, 0, false
	}

	ch := make(chan PlanEvent, planEventMaxLog+1)
	subID := e.nextSubID
	e.nextSubID++
	completed := e.Status != "running"

	// Snapshot existing events for replay
	replay := make([]PlanEvent, len(e.Events))
	copy(replay, e.Events)

	if !completed {
		e.subscribers[subID] = ch
	}
	ps.mu.Unlock()

	// Replay in a goroutine so we never block the caller.
	// Recover protects against send-on-closed-channel if Unsubscribe/Close
	// races with replay (select/default does NOT prevent this panic in Go).
	go func() {
		defer func() { recover() }()
		for _, ev := range replay {
			ch <- ev
		}
		if completed {
			close(ch)
		}
	}()

	return ch, subID, true
}

// Unsubscribe removes the subscriber and closes its channel.
func (ps *PlanStore) Unsubscribe(id string, subID uint64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	e, ok := ps.entries[id]
	if !ok {
		return
	}
	ch, exists := e.subscribers[subID]
	if !exists {
		return
	}
	delete(e.subscribers, subID)
	close(ch)
}

// Publish appends event to the plan log and fans out to subscribers (dropping slow ones).
func (ps *PlanStore) Publish(id string, event PlanEvent) {
	ps.mu.Lock()
	e, ok := ps.entries[id]
	if !ok {
		ps.mu.Unlock()
		return
	}
	if len(e.Events) < planEventMaxLog {
		e.Events = append(e.Events, event)
	}
	// Snapshot subscriber channels
	subs := make([]chan PlanEvent, 0, len(e.subscribers))
	for _, ch := range e.subscribers {
		subs = append(subs, ch)
	}
	ps.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// drop if slow
		}
	}
}

// Complete marks the plan as done, writes the done event to the log first,
// then fans out to all subscribers and closes their channels.
func (ps *PlanStore) Complete(id string, result *PlanResult) {
	now := time.Now()
	status := "complete"
	if result != nil && result.HaltedAt != nil {
		status = "partial"
	}
	if result != nil && result.Status != "" {
		status = result.Status
	}

	donePayload := map[string]any{
		"status":    status,
		"completed": result.Completed,
		"total":     result.Total,
	}
	if result.HaltedAt != nil {
		donePayload["halted_at"] = *result.HaltedAt
	}
	doneData, _ := json.Marshal(donePayload)
	doneEvent := PlanEvent{Type: "done", Data: doneData}

	ps.mu.Lock()
	e, ok := ps.entries[id]
	if !ok {
		ps.mu.Unlock()
		return
	}
	// Store done event in log first
	if len(e.Events) < planEventMaxLog {
		e.Events = append(e.Events, doneEvent)
	}
	e.Status = status
	e.Result = result
	e.CompletedAt = &now

	// Snapshot and clear subscribers
	subs := e.subscribers
	e.subscribers = make(map[uint64]chan PlanEvent)
	ps.mu.Unlock()

	// Fan out done event and close
	for _, ch := range subs {
		select {
		case ch <- doneEvent:
		default:
		}
		close(ch)
	}
}

// Close shuts down the store: closes all subscriber channels and marks the store closed.
func (ps *PlanStore) Close() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.closed {
		return
	}
	ps.closed = true
	for _, e := range ps.entries {
		for _, ch := range e.subscribers {
			close(ch)
		}
		e.subscribers = make(map[uint64]chan PlanEvent)
	}
}

// evictLocked removes completed plans (LRU) when the store is at capacity.
// Must be called with ps.mu held.
func (ps *PlanStore) evictLocked() {
	for len(ps.entries) >= planStoreMaxSize {
		// Walk order oldest-first and evict the first completed plan
		evicted := false
		for i, id := range ps.order {
			e, ok := ps.entries[id]
			if !ok {
				// stale order entry — clean up
				ps.order = append(ps.order[:i], ps.order[i+1:]...)
				evicted = true
				break
			}
			if e.Status != "running" {
				ps.order = append(ps.order[:i], ps.order[i+1:]...)
				delete(ps.entries, id)
				evicted = true
				break
			}
		}
		if !evicted {
			// All running — forcibly evict oldest
			if len(ps.order) > 0 {
				id := ps.order[0]
				ps.order = ps.order[1:]
				ps.deleteLocked(id)
			}
			break
		}
	}
}

// deleteLocked removes an entry and closes any remaining subscribers.
// Must be called with ps.mu held.
func (ps *PlanStore) deleteLocked(id string) {
	e, ok := ps.entries[id]
	if !ok {
		return
	}
	for _, ch := range e.subscribers {
		close(ch)
	}
	delete(ps.entries, id)
	// Remove from order slice to prevent stale ID accumulation.
	for i, oid := range ps.order {
		if oid == id {
			ps.order = append(ps.order[:i], ps.order[i+1:]...)
			break
		}
	}
}
