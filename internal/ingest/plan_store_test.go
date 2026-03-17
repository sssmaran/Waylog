package ingest

import (
	"testing"
	"time"
)

func TestPlanStore_CreateAndGet(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()
	if id == "" {
		t.Fatal("Create returned empty ID")
	}
	e, ok := ps.Get(id)
	if !ok {
		t.Fatal("Get returned false for newly created plan")
	}
	if e.ID != id {
		t.Errorf("entry ID mismatch: got %q want %q", e.ID, id)
	}
	if e.Status != "running" {
		t.Errorf("expected status 'running', got %q", e.Status)
	}
}

func TestPlanStore_GetUnknown(t *testing.T) {
	ps := NewPlanStore()
	_, ok := ps.Get("nonexistent")
	if ok {
		t.Fatal("Get should return false for unknown ID")
	}
}

func TestPlanStore_Publish(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()

	ch, _, ok := ps.Subscribe(id)
	if !ok {
		t.Fatal("Subscribe failed")
	}

	ev := PlanEvent{Type: "step_start", Data: []byte(`{"step":1}`)}
	ps.Publish(id, ev)

	select {
	case got := <-ch:
		if got.Type != ev.Type {
			t.Errorf("expected type %q got %q", ev.Type, got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestPlanStore_Complete(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()

	ch, _, ok := ps.Subscribe(id)
	if !ok {
		t.Fatal("Subscribe failed")
	}

	result := &PlanResult{Status: "complete", Completed: 3, Total: 3}
	ps.Complete(id, result)

	// Drain channel until closed
	var events []PlanEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Must have received at least one done event
	found := false
	for _, ev := range events {
		if ev.Type == "done" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'done' event, got %v", events)
	}

	e, ok := ps.Get(id)
	if !ok {
		t.Fatal("Get after Complete returned false")
	}
	if e.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", e.Status)
	}
	if e.Result == nil {
		t.Fatal("Result is nil after Complete")
	}
}

func TestPlanStore_LateSubscriberReplay(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()

	// Publish some events then complete
	ps.Publish(id, PlanEvent{Type: "step_start", Data: []byte(`{}`)})
	ps.Publish(id, PlanEvent{Type: "step_complete", Data: []byte(`{}`)})
	ps.Complete(id, &PlanResult{Status: "complete", Completed: 1, Total: 1})

	// Late subscriber — plan is already complete
	ch, _, ok := ps.Subscribe(id)
	if !ok {
		t.Fatal("Subscribe on completed plan returned false")
	}

	var events []PlanEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) < 3 {
		t.Errorf("expected at least 3 replayed events (2 steps + done), got %d: %v", len(events), events)
	}
	last := events[len(events)-1]
	if last.Type != "done" {
		t.Errorf("last replayed event should be 'done', got %q", last.Type)
	}
}

func TestPlanStore_TTLExpiry(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()
	ps.Complete(id, &PlanResult{Status: "complete"})

	// Manually backdate CompletedAt past TTL
	ps.mu.Lock()
	entry := ps.entries[id]
	past := time.Now().Add(-(planStoreTTL + time.Second))
	entry.CompletedAt = &past
	ps.mu.Unlock()

	_, ok := ps.Get(id)
	if ok {
		t.Fatal("Get should return false for TTL-expired plan")
	}
}

func TestPlanStore_Unsubscribe(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()

	ch, subID, ok := ps.Subscribe(id)
	if !ok {
		t.Fatal("Subscribe failed")
	}

	// Unsubscribe should not panic
	ps.Unsubscribe(id, subID)

	// Channel should be closed
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("channel should be closed after Unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close")
	}

	// Publish after unsubscribe must not panic
	ps.Publish(id, PlanEvent{Type: "step_start"})
}

func TestPlanStore_MaxEntries(t *testing.T) {
	ps := NewPlanStore()
	var firstID string

	// Create planStoreMaxSize+1 completed plans
	for i := 0; i <= planStoreMaxSize; i++ {
		id := ps.Create()
		ps.Complete(id, &PlanResult{Status: "complete"})
		if i == 0 {
			firstID = id
		}
	}

	// The first entry should have been evicted
	ps.mu.Lock()
	_, exists := ps.entries[firstID]
	ps.mu.Unlock()
	if exists {
		t.Errorf("first entry %q should have been evicted after exceeding max size", firstID)
	}

	ps.mu.Lock()
	size := len(ps.entries)
	ps.mu.Unlock()
	if size > planStoreMaxSize {
		t.Errorf("store has %d entries, expected at most %d", size, planStoreMaxSize)
	}
}

func TestPlanStore_Close(t *testing.T) {
	ps := NewPlanStore()
	id := ps.Create()

	ch, _, ok := ps.Subscribe(id)
	if !ok {
		t.Fatal("Subscribe failed")
	}

	ps.Close()

	// Subscriber channel should be closed
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("channel should be closed after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close after Close")
	}

	// New subscriptions should fail
	_, _, ok = ps.Subscribe(id)
	if ok {
		t.Fatal("Subscribe after Close should return false")
	}
}
