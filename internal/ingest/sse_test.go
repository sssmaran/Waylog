package ingest

import (
	"testing"
)

func TestSSEHub_SubscribeUnsubscribe(t *testing.T) {
	hub := NewSSEHub(10)

	id, ch, err := hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	hub.Unsubscribe(id)

	// Publish after unsubscribe should not panic.
	hub.Publish("overview", []byte(`{"ok":true}`))
}

func TestSSEHub_FanOut(t *testing.T) {
	hub := NewSSEHub(10)

	id1, ch1, err := hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	id2, ch2, err := hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}

	data := []byte(`{"count":42}`)
	hub.Publish("overview", data)

	// Both channels should be notified.
	select {
	case <-ch1:
	default:
		t.Fatal("subscriber 1 not notified")
	}
	select {
	case <-ch2:
	default:
		t.Fatal("subscriber 2 not notified")
	}

	// Both should see the data via Latest.
	lat1 := hub.Latest(id1)
	if got := string(lat1["overview"]); got != string(data) {
		t.Fatalf("subscriber 1: got %q, want %q", got, string(data))
	}
	lat2 := hub.Latest(id2)
	if got := string(lat2["overview"]); got != string(data) {
		t.Fatalf("subscriber 2: got %q, want %q", got, string(data))
	}
}

func TestSSEHub_PerSubscriberCoalescing(t *testing.T) {
	hub := NewSSEHub(10)

	id, ch, err := hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	hub.Publish("overview", []byte(`{"v":1}`))
	hub.Publish("overview", []byte(`{"v":2}`))

	// Drain the notification channel (may have 1 item since cap=1).
	select {
	case <-ch:
	default:
	}

	lat := hub.Latest(id)
	if got := string(lat["overview"]); got != `{"v":2}` {
		t.Fatalf("coalescing: got %q, want %q", got, `{"v":2}`)
	}

	// After Latest, pending should be empty.
	lat2 := hub.Latest(id)
	if len(lat2) != 0 {
		t.Fatalf("expected empty after drain, got %d topics", len(lat2))
	}
}

func TestSSEHub_MaxClients(t *testing.T) {
	hub := NewSSEHub(2)

	id1, _, err := hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	_, _, err = hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}

	// Third should fail.
	_, _, err = hub.Subscribe()
	if err != ErrMaxClients {
		t.Fatalf("expected ErrMaxClients, got %v", err)
	}

	// Unsubscribe frees a slot.
	hub.Unsubscribe(id1)

	_, _, err = hub.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe after unsubscribe: %v", err)
	}
}

func TestSSEHub_PublishNoSubscribers(t *testing.T) {
	hub := NewSSEHub(10)
	// Must not panic.
	hub.Publish("overview", []byte(`{}`))
}

func TestSSEHub_DirtyTopics(t *testing.T) {
	hub := NewSSEHub(10)

	hub.MarkDirty("overview", "routes")
	hub.MarkDirty("overview") // duplicate

	dirty := hub.DrainDirty()
	if len(dirty) != 2 {
		t.Fatalf("expected 2 dirty topics, got %d: %v", len(dirty), dirty)
	}

	got := make(map[string]bool)
	for _, d := range dirty {
		got[d] = true
	}
	if !got["overview"] || !got["routes"] {
		t.Fatalf("unexpected dirty set: %v", dirty)
	}

	// After drain, should be empty.
	dirty2 := hub.DrainDirty()
	if len(dirty2) != 0 {
		t.Fatalf("expected empty after drain, got %v", dirty2)
	}
}
