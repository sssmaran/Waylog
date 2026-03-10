package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
)

func TestIngest_HappyPath(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	body := IngestRequest{Events: []agentobs.AgentEvent{{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	}}}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)

	if rr.Code != 202 {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp IngestResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Accepted != 1 {
		t.Fatalf("expected 1 accepted, got %d", resp.Accepted)
	}
}

func TestIngest_Dedup(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	ev := agentobs.AgentEvent{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	}
	body := IngestRequest{Events: []agentobs.AgentEvent{ev, ev}}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)

	var resp IngestResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Accepted != 1 {
		t.Fatalf("expected 1 accepted, got %d", resp.Accepted)
	}
	if resp.Duplicated != 1 {
		t.Fatalf("expected 1 duplicated, got %d", resp.Duplicated)
	}
}

func TestIngest_ValidationError(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	body := IngestRequest{Events: []agentobs.AgentEvent{{
		EventID: "e1", RunID: "r1", EventType: "bad.type",
		Timestamp: time.Now(), SchemaVersion: "1.0",
	}}}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)

	var resp IngestResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Rejected != 1 {
		t.Fatalf("expected 1 rejected, got %d", resp.Rejected)
	}
	if len(resp.Errors) != 1 {
		t.Fatalf("expected 1 error detail, got %d", len(resp.Errors))
	}
}

func TestIngest_Auth(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{APIKey: "secret"})

	body := IngestRequest{Events: []agentobs.AgentEvent{{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	}}}
	b, _ := json.Marshal(body)

	// No auth
	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected 401 without auth, got %d", rr.Code)
	}

	// With Bearer auth
	req2 := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer secret")
	rr2 := httptest.NewRecorder()
	h.Ingest(rr2, req2)
	if rr2.Code != 202 {
		t.Fatalf("expected 202 with auth, got %d", rr2.Code)
	}
}

func TestIngest_MethodNotAllowed(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/v1/agent/events", nil)
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestIngest_MergeRejectionClearsDedupMark(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})
	now := time.Now()

	// First: create run and session, then end the session
	setup := IngestRequest{Events: []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r1", SessionID: "s1", EventType: agentobs.EventSessionStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0", AgentName: "a"},
		{EventID: "e3", RunID: "r1", SessionID: "s1", EventType: agentobs.EventSessionEnd, Timestamp: now.Add(2 * time.Second), SchemaVersion: "1.0", Success: true},
	}}
	b, _ := json.Marshal(setup)
	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)

	// Now send a step.start to the terminated session — merge will reject it
	bad := IngestRequest{Events: []agentobs.AgentEvent{{
		EventID: "e4", RunID: "r1", SessionID: "s1", StepID: "st1",
		EventType: agentobs.EventStepStart, StepIndex: 0,
		Timestamp: now.Add(3 * time.Second), SchemaVersion: "1.0",
	}}}
	b2, _ := json.Marshal(bad)
	req2 := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b2))
	rr2 := httptest.NewRecorder()
	h.Ingest(rr2, req2)

	var resp IngestResponse
	json.NewDecoder(rr2.Body).Decode(&resp)
	if resp.Rejected != 1 {
		t.Fatalf("expected 1 rejected, got %d", resp.Rejected)
	}

	// Retry the same event_id — should NOT be marked as duplicate
	b3, _ := json.Marshal(bad)
	req3 := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b3))
	rr3 := httptest.NewRecorder()
	h.Ingest(rr3, req3)

	var resp2 IngestResponse
	json.NewDecoder(rr3.Body).Decode(&resp2)
	if resp2.Duplicated != 0 {
		t.Fatalf("merge-rejected event should not be marked as duplicate, got %d duplicated", resp2.Duplicated)
	}
}

func TestIngest_WALFailureClearsDedupMark(t *testing.T) {
	s := store.New()
	// Create a WAL writer pointing to a read-only directory to force write failure
	dir := t.TempDir()
	w, err := eventlog.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Close the writer to make subsequent writes fail
	w.Close()

	h := NewHandler(s, w, nil, HandlerConfig{})

	body := IngestRequest{Events: []agentobs.AgentEvent{{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	}}}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/v1/agent/events", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.Ingest(rr, req)

	var resp IngestResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Rejected != 1 {
		t.Fatalf("expected 1 rejected on WAL failure, got %d", resp.Rejected)
	}

	// Verify dedup mark was cleared — retry should not be duplicate
	h.dedupMu.RLock()
	isDup := h.dedup["e1"]
	h.dedupMu.RUnlock()
	if isDup {
		t.Fatal("WAL-failed event should not remain in dedup index")
	}
}
