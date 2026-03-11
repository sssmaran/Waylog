package agentobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_SingleAgent(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Events []map[string]any }
		json.NewDecoder(r.Body).Decode(&req)
		received.Add(int32(len(req.Events)))
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{
			"accepted": len(req.Events), "duplicated": 0, "rejected": 0, "errors": []any{},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL,
		WithFlushInterval(50*time.Millisecond),
		WithBatchSize(100),
	)

	ctx := context.Background()
	run, session := client.StartSingleAgent(ctx, "test-agent")

	step := session.Step("analyze")
	step.SetModel("claude-sonnet-4-6")
	step.SetTokensIn(100)
	step.SetTokensOut(50)
	step.RecordToolCall("blast_radius", nil, nil, nil)
	step.End(ctx)

	session.End(ctx, true, "")
	run.End(ctx, true, "")

	client.Close(ctx)

	if received.Load() < 5 {
		// run.start, session.start, step.start, step.end, session.end, run.end = 6
		t.Fatalf("expected >=5 events sent, got %d", received.Load())
	}
}

func TestClient_Delegate(t *testing.T) {
	var events []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Events []map[string]any }
		json.NewDecoder(r.Body).Decode(&req)
		events = append(events, req.Events...)
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{
			"accepted": len(req.Events), "duplicated": 0, "rejected": 0, "errors": []any{},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, WithFlushInterval(50*time.Millisecond))
	ctx := context.Background()

	run := client.StartRun(ctx, "workflow")
	parent := run.StartSession(ctx, "planner")
	step := parent.Step("plan")
	step.End(ctx)

	child := parent.Delegate(ctx, "executor", step)
	childStep := child.Step("execute")
	childStep.End(ctx)
	child.End(ctx, true, "")
	parent.End(ctx, true, "")
	run.End(ctx, true, "")
	client.Close(ctx)

	// Verify parent_session_id was set
	found := false
	for _, ev := range events {
		if ev["event_type"] == "session.start" && ev["parent_session_id"] != nil && ev["parent_session_id"] != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected child session with parent_session_id")
	}
}

func TestClient_Heartbeat(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Events []map[string]any }
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		events = append(events, req.Events...)
		mu.Unlock()
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{
			"accepted": len(req.Events), "duplicated": 0, "rejected": 0, "errors": []any{},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL,
		WithFlushInterval(50*time.Millisecond),
		WithHeartbeatInterval(80*time.Millisecond),
	)
	ctx := context.Background()

	run, session := client.StartSingleAgent(ctx, "hb-agent")
	// Wait long enough for at least one heartbeat
	time.Sleep(200 * time.Millisecond)
	session.End(ctx, true, "")
	run.End(ctx, true, "")
	client.Close(ctx)

	mu.Lock()
	defer mu.Unlock()
	hbCount := 0
	for _, ev := range events {
		if ev["event_type"] == "session.heartbeat" {
			hbCount++
		}
	}
	if hbCount == 0 {
		t.Fatal("expected at least one heartbeat event")
	}
}

func TestClient_CloseStopsHeartbeats(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Events []map[string]any }
		json.NewDecoder(r.Body).Decode(&req)
		received.Add(int32(len(req.Events)))
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{
			"accepted": len(req.Events), "duplicated": 0, "rejected": 0, "errors": []any{},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL,
		WithFlushInterval(50*time.Millisecond),
		WithHeartbeatInterval(50*time.Millisecond),
	)
	ctx := context.Background()

	run := client.StartRun(ctx, "leak-test")
	_ = run.StartSession(ctx, "leaky-agent") // intentionally not ended

	// Close without ending session — heartbeats must stop
	client.Close(ctx)
	countAfterClose := received.Load()

	// Wait and verify no more events arrive
	time.Sleep(200 * time.Millisecond)
	if received.Load() > countAfterClose {
		t.Fatal("heartbeat goroutine continued emitting after Client.Close")
	}
}
