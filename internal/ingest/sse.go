package ingest

import (
	"errors"
	"sync"
)

// SSE topic names.
const (
	TopicOverview    = "overview"
	TopicTimeseries  = "timeseries"
	TopicRoutes      = "routes"
	TopicDeployments = "deployments"
)

// ErrMaxClients is returned by Subscribe when the hub is at capacity.
var ErrMaxClients = errors.New("sse: max clients reached")

// subscriber holds a notification channel and per-topic latest values.
type subscriber struct {
	ch     chan struct{}
	mu     sync.Mutex
	latest map[string][]byte // topic → most recent snapshot
}

// SSEHub is a pure pub/sub fan-out hub with per-subscriber coalescing.
// It has no HTTP awareness. Every published value is a complete state
// snapshot for its topic.
type SSEHub struct {
	mu        sync.RWMutex
	subs      map[uint64]*subscriber
	nextID    uint64
	maxClient int

	dirtyMu sync.Mutex
	dirty   map[string]struct{}
}

// NewSSEHub creates a hub that allows at most maxClients concurrent subscribers.
func NewSSEHub(maxClients int) *SSEHub {
	return &SSEHub{
		subs:      make(map[uint64]*subscriber),
		maxClient: maxClients,
		dirty:     make(map[string]struct{}),
	}
}

// Subscribe registers a new subscriber. It returns the subscriber ID,
// a notification channel (capacity 1), and an error if the hub is full.
func (h *SSEHub) Subscribe() (uint64, <-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.subs) >= h.maxClient {
		return 0, nil, ErrMaxClients
	}

	h.nextID++
	id := h.nextID
	ch := make(chan struct{}, 1)
	h.subs[id] = &subscriber{
		ch:     ch,
		latest: make(map[string][]byte),
	}
	return id, ch, nil
}

// Unsubscribe removes a subscriber. Safe to call with an unknown ID.
func (h *SSEHub) Unsubscribe(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, id)
}

// Publish fans out data to all subscribers for the given topic.
// Each subscriber's latest value for the topic is overwritten (coalesced),
// and its notification channel is poked non-blocking.
func (h *SSEHub) Publish(topic string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		sub.mu.Lock()
		sub.latest[topic] = data
		sub.mu.Unlock()
		// Non-blocking poke.
		select {
		case sub.ch <- struct{}{}:
		default:
		}
	}
}

// Latest returns and clears all pending topic values for a subscriber.
// Returns nil if the subscriber ID is unknown.
func (h *SSEHub) Latest(id uint64) map[string][]byte {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sub, ok := h.subs[id]
	if !ok {
		return nil
	}

	sub.mu.Lock()
	defer sub.mu.Unlock()

	if len(sub.latest) == 0 {
		return nil
	}

	out := sub.latest
	sub.latest = make(map[string][]byte)
	return out
}

// MarkDirty marks topics for recomputation by a ticker.
func (h *SSEHub) MarkDirty(topics ...string) {
	h.dirtyMu.Lock()
	defer h.dirtyMu.Unlock()
	for _, t := range topics {
		h.dirty[t] = struct{}{}
	}
}

// DrainDirty returns and clears the set of dirty topics.
func (h *SSEHub) DrainDirty() []string {
	h.dirtyMu.Lock()
	defer h.dirtyMu.Unlock()

	if len(h.dirty) == 0 {
		return nil
	}

	out := make([]string, 0, len(h.dirty))
	for t := range h.dirty {
		out = append(out, t)
	}
	h.dirty = make(map[string]struct{})
	return out
}
