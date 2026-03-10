package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
)

func seedRunSessionStep(t *testing.T, s *store.Store, now time.Time) {
	t.Helper()
	events := []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r1", SessionID: "s1", EventType: agentobs.EventSessionStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0", AgentName: "coder"},
		{EventID: "e3", RunID: "r1", SessionID: "s1", StepID: "st1", StepIndex: 0, StepName: "think", EventType: agentobs.EventStepStart, Timestamp: now.Add(2 * time.Second), SchemaVersion: "1.0"},
		{EventID: "e4", RunID: "r1", SessionID: "s1", StepID: "st1", StepIndex: 0, EventType: agentobs.EventStepEnd, Timestamp: now.Add(3 * time.Second), SchemaVersion: "1.0", Model: "gpt-4", TokensIn: 100, TokensOut: 50, ToolName: "bash", ToolInput: "ls", ToolOutput: "file.go", LatencyMs: 500},
	}
	for _, ev := range events {
		if err := s.Merge(&ev); err != nil {
			t.Fatalf("seed merge failed: %v", err)
		}
	}
}

func TestListRuns(t *testing.T) {
	s := store.New()
	now := time.Now()

	// Seed 2 runs
	for _, ev := range []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r2", EventType: agentobs.EventRunStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0"},
	} {
		if err := s.Merge(&ev); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/runs", nil)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var runs []store.RunInfo
	json.Unmarshal(resp["runs"], &runs)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
}

func TestGetRun_NotFound(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	req := httptest.NewRequest("GET", "/v1/agent/runs/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetRun_HappyPath(t *testing.T) {
	s := store.New()
	now := time.Now()

	events := []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r1", SessionID: "s1", EventType: agentobs.EventSessionStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0", AgentName: "coder"},
	}
	for _, ev := range events {
		if err := s.Merge(&ev); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/runs/r1", nil)
	req.SetPathValue("id", "r1")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var run store.RunInfo
	json.Unmarshal(resp["run"], &run)
	if run.RunID != "r1" {
		t.Fatalf("expected run_id r1, got %s", run.RunID)
	}

	var sessions []store.SessionInfo
	json.Unmarshal(resp["sessions"], &sessions)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
}

func TestGetSession(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedRunSessionStep(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/sessions/s1", nil)
	req.SetPathValue("id", "s1")
	rr := httptest.NewRecorder()
	h.GetSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var sess store.SessionInfo
	json.Unmarshal(resp["session"], &sess)
	if sess.SessionID != "s1" {
		t.Fatalf("expected session_id s1, got %s", sess.SessionID)
	}

	var steps []store.StepInfo
	json.Unmarshal(resp["steps"], &steps)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].ToolName != "bash" {
		t.Fatalf("expected tool bash, got %s", steps[0].ToolName)
	}
}

func TestGetStats(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedRunSessionStep(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/stats?window=1h", nil)
	rr := httptest.NewRecorder()
	h.GetStats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var stats store.Stats
	json.NewDecoder(rr.Body).Decode(&stats)
	if stats.RunCount != 1 {
		t.Fatalf("expected 1 run, got %d", stats.RunCount)
	}
	if stats.SessionCount != 1 {
		t.Fatalf("expected 1 session, got %d", stats.SessionCount)
	}
	if stats.StepCount != 1 {
		t.Fatalf("expected 1 step, got %d", stats.StepCount)
	}
}

func TestGetCost(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedRunSessionStep(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{
		CostRates: map[string]CostRate{
			"gpt-4": {InputPer1K: 0.03, OutputPer1K: 0.06},
		},
	})
	req := httptest.NewRequest("GET", "/v1/agent/cost?window=1h", nil)
	rr := httptest.NewRecorder()
	h.GetCost(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var models []struct {
		Model     string  `json:"model"`
		TokensIn  int     `json:"tokens_in"`
		TokensOut int     `json:"tokens_out"`
		Total     float64 `json:"total"`
	}
	json.Unmarshal(resp["models"], &models)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].Model != "gpt-4" {
		t.Fatalf("expected model gpt-4, got %s", models[0].Model)
	}
	if models[0].TokensIn != 100 {
		t.Fatalf("expected 100 tokens_in, got %d", models[0].TokensIn)
	}
	// cost_in = 100/1000 * 0.03 = 0.003, cost_out = 50/1000 * 0.06 = 0.003, total = 0.006
	expectedTotal := 0.006
	if models[0].Total < expectedTotal-0.0001 || models[0].Total > expectedTotal+0.0001 {
		t.Fatalf("expected total ~%f, got %f", expectedTotal, models[0].Total)
	}
}

func TestGetToolAnalytics(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedRunSessionStep(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/tools?window=1h", nil)
	rr := httptest.NewRecorder()
	h.GetToolAnalytics(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var tools []store.ToolStat
	json.Unmarshal(resp["tools"], &tools)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].ToolName != "bash" {
		t.Fatalf("expected tool bash, got %s", tools[0].ToolName)
	}
	if tools[0].CallCount != 1 {
		t.Fatalf("expected 1 call, got %d", tools[0].CallCount)
	}
}

func TestReadHandlers_MethodNotAllowed(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	handlers := []struct {
		name    string
		handler http.HandlerFunc
		path    string
	}{
		{"ListRuns", h.ListRuns, "/v1/agent/runs"},
		{"GetRun", h.GetRun, "/v1/agent/runs/r1"},
		{"GetSession", h.GetSession, "/v1/agent/sessions/s1"},
		{"GetWaterfall", h.GetWaterfall, "/v1/agent/sessions/s1/waterfall"},
		{"GetStats", h.GetStats, "/v1/agent/stats"},
		{"GetCost", h.GetCost, "/v1/agent/cost"},
		{"GetToolAnalytics", h.GetToolAnalytics, "/v1/agent/tools"},
	}

	for _, tc := range handlers {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d", rr.Code)
			}
		})
	}
}

func TestListRuns_InvalidCursor(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	// Invalid base64
	req := httptest.NewRequest("GET", "/v1/agent/runs?before=not-valid-base64!!!", nil)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid base64 cursor, got %d", rr.Code)
	}

	// Valid base64 but invalid timestamp
	badTime := base64.URLEncoding.EncodeToString([]byte("not-a-timestamp"))
	req2 := httptest.NewRequest("GET", "/v1/agent/runs?before="+badTime, nil)
	rr2 := httptest.NewRecorder()
	h.ListRuns(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid timestamp cursor, got %d", rr2.Code)
	}
}

func TestListRuns_ValidCursor(t *testing.T) {
	s := store.New()
	now := time.Now()
	for _, ev := range []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r2", EventType: agentobs.EventRunStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0"},
	} {
		if err := s.Merge(&ev); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandler(s, nil, nil, HandlerConfig{})
	cursor := base64.URLEncoding.EncodeToString([]byte(now.Add(2 * time.Second).Format(time.RFC3339Nano)))
	req := httptest.NewRequest("GET", "/v1/agent/runs?before="+cursor, nil)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid cursor, got %d", rr.Code)
	}
}

func TestGetWaterfall(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedRunSessionStep(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{})
	req := httptest.NewRequest("GET", "/v1/agent/sessions/s1/waterfall", nil)
	req.SetPathValue("id", "s1")
	rr := httptest.NewRecorder()
	h.GetWaterfall(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string][]WaterfallEntry
	json.NewDecoder(rr.Body).Decode(&resp)

	entries := resp["waterfall"]
	if len(entries) != 1 {
		t.Fatalf("expected 1 waterfall entry, got %d", len(entries))
	}
	if entries[0].SessionID != "s1" {
		t.Fatalf("expected session_id s1, got %s", entries[0].SessionID)
	}
	if entries[0].ToolName != "bash" {
		t.Fatalf("expected tool bash, got %s", entries[0].ToolName)
	}
	if entries[0].DurationMs != 500 {
		t.Fatalf("expected duration 500ms, got %d", entries[0].DurationMs)
	}
}

func TestGetWaterfall_NotFound(t *testing.T) {
	s := store.New()
	h := NewHandler(s, nil, nil, HandlerConfig{})

	req := httptest.NewRequest("GET", "/v1/agent/sessions/nope/waterfall", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	h.GetWaterfall(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func seedWithPrompt(t *testing.T, s *store.Store, now time.Time) {
	t.Helper()
	events := []agentobs.AgentEvent{
		{EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart, Timestamp: now, SchemaVersion: "1.0"},
		{EventID: "e2", RunID: "r1", SessionID: "s1", EventType: agentobs.EventSessionStart, Timestamp: now.Add(time.Second), SchemaVersion: "1.0", AgentName: "coder", Prompt: "secret prompt"},
		{EventID: "e3", RunID: "r1", SessionID: "s1", StepID: "st1", StepIndex: 0, EventType: agentobs.EventStepStart, Timestamp: now.Add(2 * time.Second), SchemaVersion: "1.0"},
		{EventID: "e4", RunID: "r1", SessionID: "s1", StepID: "st1", StepIndex: 0, EventType: agentobs.EventStepEnd, Timestamp: now.Add(3 * time.Second), SchemaVersion: "1.0", Model: "gpt-4", TokensIn: 100, TokensOut: 50, ToolName: "bash", ToolInput: "ls", ToolOutput: "file.go", LatencyMs: 500},
	}
	for _, ev := range events {
		if err := s.Merge(&ev); err != nil {
			t.Fatalf("seed merge failed: %v", err)
		}
	}
}

func TestGetSession_Redaction(t *testing.T) {
	s := store.New()
	now := time.Now()
	seedWithPrompt(t, s, now)

	h := NewHandler(s, nil, nil, HandlerConfig{RedactPayloads: true})
	req := httptest.NewRequest("GET", "/v1/agent/sessions/s1", nil)
	req.SetPathValue("id", "s1")
	rr := httptest.NewRecorder()
	h.GetSession(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&resp)

	var sess store.SessionInfo
	json.Unmarshal(resp["session"], &sess)
	if sess.Prompt != "" {
		t.Fatalf("expected redacted prompt, got %q", sess.Prompt)
	}

	var steps []store.StepInfo
	json.Unmarshal(resp["steps"], &steps)
	if len(steps) == 0 {
		t.Fatal("expected steps")
	}
	if steps[0].ToolInput != "" {
		t.Fatalf("expected redacted tool_input, got %q", steps[0].ToolInput)
	}
	if steps[0].ToolOutput != "" {
		t.Fatalf("expected redacted tool_output, got %q", steps[0].ToolOutput)
	}
}
