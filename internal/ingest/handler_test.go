package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

const testTrace = "aaaa0000bbbb1111cccc2222dddd3333"

func TestCORSWrap(t *testing.T) {
	handler := CORSWrap("http://localhost:3000", "GET, OPTIONS", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Test preflight
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS: expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("CORS origin = %q, want %q", got, "http://localhost:3000")
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("expected Access-Control-Allow-Headers to be set")
	}

	// Test normal GET
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("CORS origin = %q, want %q", got, "http://localhost:3000")
	}
}

func TestCapabilities_Defaults(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	w := httptest.NewRecorder()
	srv.Capabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Ask struct {
			Enabled         bool   `json:"enabled"`
			Model           string `json:"model"`
			ToolMode        string `json:"tool_mode"`
			MaxStepsDefault int    `json:"max_steps_default"`
			MaxStepsMax     int    `json:"max_steps_max"`
		} `json:"ask"`
		Dashboard struct {
			RefreshIntervalSec int `json:"refresh_interval_sec"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp.Ask.MaxStepsDefault != 5 {
		t.Errorf("max_steps_default = %d, want 5", resp.Ask.MaxStepsDefault)
	}
	if resp.Ask.MaxStepsMax != 8 {
		t.Errorf("max_steps_max = %d, want 8", resp.Ask.MaxStepsMax)
	}
	if resp.Dashboard.RefreshIntervalSec != 10 {
		t.Errorf("refresh_interval_sec = %d, want 10", resp.Dashboard.RefreshIntervalSec)
	}
}

func TestCapabilities_OTLPGRPCBlock(t *testing.T) {
	srv := NewServer(ServerConfig{
		OTLPEnabled:     true,
		OTLPGRPCEnabled: true,
		OTLPGRPCAddr:    ":4317",
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	w := httptest.NewRecorder()
	srv.Capabilities(w, req)

	var resp struct {
		OTLP struct {
			HTTPTraces bool   `json:"http_traces"`
			GRPCTraces bool   `json:"grpc_traces"`
			GRPCAddr   string `json:"grpc_addr"`
		} `json:"otlp"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !resp.OTLP.HTTPTraces {
		t.Fatal("otlp.http_traces = false, want true")
	}
	if !resp.OTLP.GRPCTraces {
		t.Fatal("otlp.grpc_traces = false, want true")
	}
	if resp.OTLP.GRPCAddr != ":4317" {
		t.Fatalf("otlp.grpc_addr = %q, want :4317", resp.OTLP.GRPCAddr)
	}
}

func TestCapabilities_IncidentsBlock(t *testing.T) {
	tests := []struct {
		name             string
		cfg              ServerConfig
		wantEnabled      bool
		wantPersistent   bool
		wantRebuild      bool
		wantRebuildScope string
	}{
		{name: "disabled"},
		{
			name: "sqlite enabled",
			cfg: ServerConfig{
				IncidentsEnabled:         true,
				IncidentsPersistent:      true,
				IncidentRebuildSupported: true,
			},
			wantEnabled:      true,
			wantPersistent:   true,
			wantRebuild:      true,
			wantRebuildScope: "hot-window",
		},
		{name: "requested but sqlite missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(tc.cfg)
			req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
			w := httptest.NewRecorder()
			srv.Capabilities(w, req)
			var resp struct {
				Incidents struct {
					Enabled    bool `json:"enabled"`
					Persistent bool `json:"persistent"`
					Rebuild    struct {
						Supported bool   `json:"supported"`
						Scope     string `json:"scope"`
					} `json:"rebuild"`
				} `json:"incidents"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if resp.Incidents.Enabled != tc.wantEnabled {
				t.Fatalf("enabled=%v want %v", resp.Incidents.Enabled, tc.wantEnabled)
			}
			if resp.Incidents.Persistent != tc.wantPersistent {
				t.Fatalf("persistent=%v want %v", resp.Incidents.Persistent, tc.wantPersistent)
			}
			if resp.Incidents.Rebuild.Supported != tc.wantRebuild {
				t.Fatalf("rebuild.supported=%v want %v", resp.Incidents.Rebuild.Supported, tc.wantRebuild)
			}
			if resp.Incidents.Rebuild.Scope != tc.wantRebuildScope {
				t.Fatalf("rebuild.scope=%q want %q", resp.Incidents.Rebuild.Scope, tc.wantRebuildScope)
			}
		})
	}
}

const successTrace = "bbbb0000cccc1111dddd2222eeee3333"

func newTestEventLog(dir string) (*eventlog.Writer, error) {
	return eventlog.New(dir)
}

func TestLivez(t *testing.T) {
	srv := NewServer(ServerConfig{})
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	w := httptest.NewRecorder()
	srv.Livez(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", w.Body.String())
	}
}

func TestReadyz_NotReady(t *testing.T) {
	srv := NewServer(ServerConfig{})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	srv.Readyz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestReadyz_Ready(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetReady()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	srv.Readyz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", w.Body.String())
	}
}

func TestHealth_JSON(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetReady()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Health(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want 'ok'", resp["status"])
	}
	if resp["ready"] != true {
		t.Errorf("ready = %v, want true", resp["ready"])
	}
	for _, key := range []string{"status", "uptime", "ready", "event_log", "replay"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	replayInfo := resp["replay"].(map[string]any)
	if replayInfo["status"] != "none" {
		t.Errorf("replay.status = %q, want 'none'", replayInfo["status"])
	}
}

func TestHealth_ReplaySuccess(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetReplayResult(nil)
	srv.SetReady()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Health(w, req)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["status"] != "ok" {
		t.Errorf("status = %q, want 'ok'", resp["status"])
	}
	replay := resp["replay"].(map[string]any)
	if replay["status"] != "ok" {
		t.Errorf("replay.status = %q, want 'ok'", replay["status"])
	}
	if _, ok := replay["error"]; ok {
		t.Error("replay.error should not be present on success")
	}
	if _, ok := replay["last_attempt"]; !ok {
		t.Error("missing replay.last_attempt")
	}
	if _, ok := replay["last_success"]; !ok {
		t.Error("missing replay.last_success")
	}
}

func TestHealth_ReplayFailed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetReplayResult(errors.New("corrupt eventlog"))
	srv.SetReady()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Health(w, req)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["status"] != "degraded" {
		t.Errorf("status = %q, want 'degraded'", resp["status"])
	}
	if resp["ready"] != true {
		t.Errorf("ready = %v, want true (fail-open)", resp["ready"])
	}
	replay := resp["replay"].(map[string]any)
	if replay["status"] != "failed" {
		t.Errorf("replay.status = %q, want 'failed'", replay["status"])
	}
	if replay["error"] != "corrupt eventlog" {
		t.Errorf("replay.error = %q, want 'corrupt eventlog'", replay["error"])
	}
	if _, ok := replay["last_attempt"]; !ok {
		t.Error("missing replay.last_attempt")
	}
	if _, ok := replay["last_success"]; ok {
		t.Error("replay.last_success should not be present on failure")
	}
}

func keepAllSampler() *sampler.Sampler {
	return sampler.New(sampler.Config{HappySampleRatePct: 100})
}

func gatherMap(families []*dto.MetricFamily) map[string]*dto.MetricFamily {
	m := make(map[string]*dto.MetricFamily, len(families))
	for _, f := range families {
		m[f.GetName()] = f
	}
	return m
}

func counterValue(mf *dto.MetricFamily) float64 {
	if mf == nil {
		return 0
	}
	for _, m := range mf.GetMetric() {
		return m.GetCounter().GetValue()
	}
	return 0
}

func histogramCount(mf *dto.MetricFamily) uint64 {
	if mf == nil {
		return 0
	}
	for _, m := range mf.GetMetric() {
		return m.GetHistogram().GetSampleCount()
	}
	return 0
}

func TestCapabilities_Architecture(t *testing.T) {
	t.Setenv("GRAPH_HOT_WINDOW", "90m")

	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	w := httptest.NewRecorder()
	srv.Capabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, present := resp["graph"]; present {
		t.Fatalf("capabilities.graph field should be removed, got %#v", resp["graph"])
	}

	arch, ok := resp["architecture"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture capability block: %#v", resp["architecture"])
	}
	if flattened, ok := arch["flattened"].(bool); !ok || !flattened {
		t.Fatalf("architecture.flattened = %v, want true", arch["flattened"])
	}
	hotWindow, ok := arch["hot_window"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture.hot_window block: %#v", arch["hot_window"])
	}
	if enabled, ok := hotWindow["enabled"].(bool); !ok || !enabled {
		t.Fatalf("architecture.hot_window.enabled = %v, want true", hotWindow["enabled"])
	}
	if source, ok := hotWindow["source"].(string); !ok || source != "GRAPH_HOT_WINDOW" {
		t.Fatalf("architecture.hot_window.source = %v, want GRAPH_HOT_WINDOW", hotWindow["source"])
	}
	if duration, ok := hotWindow["duration"].(string); !ok || duration != "1h30m0s" {
		t.Fatalf("architecture.hot_window.duration = %v, want 1h30m0s", hotWindow["duration"])
	}
	if secs, ok := hotWindow["duration_secs"].(float64); !ok || int64(secs) != 5400 {
		t.Fatalf("architecture.hot_window.duration_secs = %v, want 5400", hotWindow["duration_secs"])
	}
}

// --- Agentic API fix tests ---

func TestAsk_InvalidJSON_EnvelopeError(t *testing.T) {
	srv := &Server{maxBodyBytes: 1 << 20}
	r := httptest.NewRequest("POST", "/v1/ask?envelope=v2", strings.NewReader("{bad"))
	r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.Ask(w, r)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error in envelope")
	}
	if resp.Error.Code != "INVALID_PARAMS" {
		t.Errorf("code = %q, want INVALID_PARAMS", resp.Error.Code)
	}
}

func TestToolCall_InvalidJSON_EnvelopeError(t *testing.T) {
	reg := tools.NewRegistry()
	srv := &Server{maxBodyBytes: 1 << 20, askRegistry: reg}
	r := httptest.NewRequest("POST", "/v1/tools/explain_request?envelope=v2", strings.NewReader("{bad"))
	r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.ToolCall(w, r)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != "INVALID_PARAMS" {
		t.Errorf("expected INVALID_PARAMS, got %+v", resp.Error)
	}
}

func TestPlanExecute_TriageTemplateExecutesAsPlan(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "triage_incident",
		Description: "test triage",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"required":["incident_id"],
			"properties":{
				"incident_id":{"type":"string"},
				"window":{"type":"string"},
				"snapshot":{"type":"boolean"}
			}
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			var got struct {
				IncidentID string `json:"incident_id"`
				Window     string `json:"window"`
				Snapshot   bool   `json:"snapshot"`
			}
			if err := json.Unmarshal(params, &got); err != nil {
				return nil, err
			}
			return map[string]any{
				"schema_version": "triage.v1",
				"incident_ref":   map[string]string{"id": got.IncidentID, "window": got.Window},
				"report_hash":    "sha256:test",
				"snapshot":       got.Snapshot,
			}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ps := NewPlanStore()
	srv := &Server{maxBodyBytes: 1 << 20, askRegistry: reg, planStore: ps}
	body := `{"template":"triage","params":{"incident_id":"inc_abc","window":"15m","snapshot":true}}`
	r := httptest.NewRequest(http.MethodPost, "/v1/plans/execute", strings.NewReader(body))
	r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.PlanExecute(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Plan-ID") == "" {
		t.Fatalf("missing X-Plan-ID")
	}
	var result PlanResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "complete" || result.Completed != 1 || result.Total != 1 {
		t.Fatalf("result status = %+v", result)
	}
	if result.Steps[0].ID != "triage" || result.Steps[0].Tool != "triage_incident" {
		t.Fatalf("step = %+v", result.Steps[0])
	}
	raw, err := json.Marshal(result.Steps[0].Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var rep struct {
		ReportHash  string `json:"report_hash"`
		IncidentRef struct {
			ID string `json:"id"`
		} `json:"incident_ref"`
		Snapshot bool `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if rep.ReportHash != "sha256:test" || rep.IncidentRef.ID != "inc_abc" || !rep.Snapshot {
		t.Fatalf("report = %+v", rep)
	}
	entry, ok := ps.Get(result.PlanID)
	if !ok || len(entry.Events) < 3 {
		t.Fatalf("expected SSE event log with start/complete/done, got ok=%v entry=%+v", ok, entry)
	}
}

func TestPlanExecute_TemplateValidationErrors(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "triage_incident",
		Description: "test triage",
		InputSchema: json.RawMessage(`{"type":"object","required":["incident_id"],"properties":{"incident_id":{"type":"string"}}}`),
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			return map[string]string{"ok": "true"}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv := &Server{maxBodyBytes: 1 << 20, askRegistry: reg}
	cases := map[string]string{
		"unknown template":        `{"template":"bogus","params":{"incident_id":"inc_abc"}}`,
		"missing incident id":     `{"template":"triage","params":{"snapshot":true}}`,
		"steps and template both": `{"steps":[{"id":"x","tool":"triage_incident","params":{"incident_id":"inc_abc"}}],"template":"triage","params":{"incident_id":"inc_abc"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/plans/execute?envelope=v2", strings.NewReader(body))
			r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
			w := httptest.NewRecorder()
			srv.PlanExecute(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			var resp APIResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error == nil || resp.Error.Code != "INVALID_PLAN" {
				t.Fatalf("error = %+v, want INVALID_PLAN", resp.Error)
			}
		})
	}
}

func TestAsk_DedupSafetyNet_PreservesActualStatus(t *testing.T) {
	dc := NewDedupCache()
	srv := &Server{

		maxBodyBytes: 1 << 20,
		dedupCache:   dc,
	}
	// askProvider is nil and no env key → should return 503
	body := `{"prompt":"test"}`
	r := httptest.NewRequest("POST", "/v1/ask", strings.NewReader(body))
	r.Header.Set("Idempotency-Key", "test-key-1")
	r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.Ask(w, r)

	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}

	// Replay should also get 503
	r2 := httptest.NewRequest("POST", "/v1/ask", strings.NewReader(body))
	r2.Header.Set("Idempotency-Key", "test-key-1")
	r2 = r2.WithContext(ContextWithRequestID(r2.Context(), "req_test2"))
	w2 := httptest.NewRecorder()
	srv.Ask(w2, r2)

	if w2.Code != 503 {
		t.Fatalf("replay status = %d, want 503", w2.Code)
	}
}

func TestAsk_MissingProviderMessageIsProviderAgnostic(t *testing.T) {
	t.Setenv("WAYLOG_LLM_PROVIDER", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	srv := &Server{

		maxBodyBytes: 1 << 20,
		dedupCache:   NewDedupCache(),
	}
	r := httptest.NewRequest("POST", "/v1/ask?envelope=v2", strings.NewReader(`{"prompt":"test"}`))
	w := httptest.NewRecorder()
	srv.Ask(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error response")
	}
	if got := resp.Error.Message; got != llm.ErrProviderNotConfigured.Error() {
		t.Fatalf("message = %q, want %q", got, llm.ErrProviderNotConfigured.Error())
	}
	if strings.Contains(strings.ToLower(resp.Error.Message), "gemini") {
		t.Fatalf("message should not pin Gemini: %q", resp.Error.Message)
	}
}

func TestToolCall_DedupSafetyNet_Exists(t *testing.T) {
	dc := NewDedupCache()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "explain_request",
		Description: "stub for dedup test",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, fmt.Errorf("trace not found")
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{

		maxBodyBytes: 1 << 20,
		dedupCache:   dc,
		askRegistry:  reg,
	}

	body := `{"trace_id":"nonexistent"}`
	r := httptest.NewRequest("POST", "/v1/tools/explain_request", strings.NewReader(body))
	r.Header.Set("Idempotency-Key", "tc-key-1")
	r = r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.ToolCall(w, r)

	// Replay should return same status
	r2 := httptest.NewRequest("POST", "/v1/tools/explain_request", strings.NewReader(body))
	r2.Header.Set("Idempotency-Key", "tc-key-1")
	r2 = r2.WithContext(ContextWithRequestID(r2.Context(), "req_test2"))
	w2 := httptest.NewRecorder()
	srv.ToolCall(w2, r2)

	if w.Code != w2.Code {
		t.Errorf("replay status %d != original %d", w2.Code, w.Code)
	}
}

func TestAsk_WaiterTimeout_Logs(t *testing.T) {
	dc := NewDedupCache()
	srv := &Server{maxBodyBytes: 1 << 20, dedupCache: dc}

	body := `{"prompt":"test"}`
	// Acquire inflight slot manually to force a waiter
	dc.AcquireOrWait(context.Background(), "POST", "/v1/ask", "principal", "block-key", []byte(body))

	// Second request with short timeout will abort
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure timeout fires
	r := httptest.NewRequest("POST", "/v1/ask", strings.NewReader(body))
	r = r.WithContext(ContextWithRequestID(ctx, "req_waiter"))
	r.Header.Set("Idempotency-Key", "block-key")
	w := httptest.NewRecorder()
	srv.Ask(w, r)

	// Should get 504 or no response (canceled)
	if w.Code != 504 && w.Code != 200 {
		t.Logf("waiter abort: status=%d (expected 504 or no-write)", w.Code)
	}
}

func TestNormalizeErrorCode_ProviderError(t *testing.T) {
	pe := &llm.ProviderError{Provider: "gemini", StatusCode: 429, Message: "rate limited"}
	got := normalizeErrorCode(pe)
	if got != "PROVIDER_ERROR" {
		t.Errorf("normalizeErrorCode(ProviderError) = %q, want PROVIDER_ERROR", got)
	}
}

func TestNormalizeErrorCode_WrappedProviderError(t *testing.T) {
	pe := &llm.ProviderError{Provider: "gemini", StatusCode: 500, Message: "internal"}
	wrapped := fmt.Errorf("ask: %w", pe)
	got := normalizeErrorCode(wrapped)
	if got != "PROVIDER_ERROR" {
		t.Errorf("normalizeErrorCode(wrapped ProviderError) = %q, want PROVIDER_ERROR", got)
	}
}

func TestCORSWrap_ExposesHeaders(t *testing.T) {
	handler := CORSWrap("*", "GET, OPTIONS", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	r := httptest.NewRequest("OPTIONS", "/test", nil)
	w := httptest.NewRecorder()
	handler(w, r)

	allow := w.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(allow, "X-Request-ID") {
		t.Errorf("Allow-Headers missing X-Request-ID: %q", allow)
	}
	expose := w.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(expose, "X-Request-ID") || !strings.Contains(expose, "Waylog-API-Version") {
		t.Errorf("Expose-Headers missing expected: %q", expose)
	}
}

func TestTools_MethodNotAllowed_EnvelopeError(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest("POST", "/v1/tools?envelope=v2", nil)
	req = req.WithContext(ContextWithRequestID(req.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.Tools(w, req)

	if w.Code != 405 {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	var env APIResponse
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatalf("expected JSON envelope, got: %s", w.Body.String())
	}
	if env.Error == nil || env.Error.Code != "METHOD_NOT_ALLOWED" {
		t.Errorf("expected METHOD_NOT_ALLOWED, got %+v", env.Error)
	}
}

func TestAsk_Metrics_CountedOnValidationFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	srv := &Server{metrics: m, maxBodyBytes: 1 << 20}

	// Send invalid JSON — should still count in AskRequestsTotal
	req := httptest.NewRequest("POST", "/v1/ask", strings.NewReader("not json"))
	req = req.WithContext(ContextWithRequestID(req.Context(), "req_test"))
	w := httptest.NewRecorder()
	srv.Ask(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	// Gather metrics and check AskRequestsTotal was incremented
	mfs, _ := reg.Gather()
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "waylog_ask_requests_total" {
			found = true
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "status" && lp.GetValue() != "400" {
						t.Errorf("expected status=400, got %s", lp.GetValue())
					}
				}
			}
			break
		}
	}
	if !found {
		t.Error("expected waylog_ask_requests_total to be emitted on validation failure")
	}
}

func TestAsk_Idempotency_NotEnforcedForValidationErrors(t *testing.T) {
	srv := &Server{

		maxBodyBytes: 1 << 20,
		dedupCache:   NewDedupCache(),
	}

	// Send invalid JSON with an Idempotency-Key — twice
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/ask", strings.NewReader("not json"))
		req.Header.Set("Idempotency-Key", "idem-validation-test")
		req = req.WithContext(ContextWithRequestID(req.Context(), fmt.Sprintf("req_%d", i)))
		w := httptest.NewRecorder()
		srv.Ask(w, req)

		if w.Code != 400 {
			t.Fatalf("call %d: status = %d, want 400", i, w.Code)
		}
	}

	// Cache should be empty — validation errors don't enter dedup
	if srv.dedupCache.Size() != 0 {
		t.Errorf("dedup cache size = %d, want 0 (validation errors should not be cached)", srv.dedupCache.Size())
	}
}

type stubAskProvider struct{}

func (stubAskProvider) Generate(ctx context.Context, prompt string, tools []llm.ToolDefinition, history []llm.Turn) (llm.Result, error) {
	return llm.Result{}, nil
}

func TestCapabilities_LLMBlock(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		askProvider    llm.Provider
		wantProvider   string
		wantConfigured bool
		wantAskEnabled bool
	}{
		{
			name:           "no env",
			env:            map[string]string{},
			wantProvider:   "none",
			wantConfigured: false,
			wantAskEnabled: false,
		},
		{
			name:           "gemini key set",
			env:            map[string]string{"WAYLOG_LLM_PROVIDER": "gemini", "GEMINI_API_KEY": "test-key"},
			wantProvider:   "gemini",
			wantConfigured: true,
			wantAskEnabled: true,
		},
		{
			name:           "custom injected provider",
			env:            map[string]string{},
			askProvider:    stubAskProvider{},
			wantProvider:   "custom",
			wantConfigured: true,
			wantAskEnabled: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WAYLOG_LLM_PROVIDER", "")
			t.Setenv("WAYLOG_LLM_MODEL", "")
			t.Setenv("GEMINI_API_KEY", "")
			t.Setenv("GOOGLE_API_KEY", "")
			t.Setenv("GEMINI_MODEL", "")
			t.Setenv("GEMINI_API_BASE", "")
			t.Setenv("GEMINI_TOOL_MODE", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			srv := NewServer(ServerConfig{AskProvider: tc.askProvider})
			req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
			w := httptest.NewRecorder()
			srv.Capabilities(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			var resp struct {
				LLM struct {
					Provider   string `json:"provider"`
					Model      string `json:"model"`
					ToolMode   string `json:"tool_mode"`
					Configured bool   `json:"configured"`
					AskEnabled bool   `json:"ask_enabled"`
				} `json:"llm"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if resp.LLM.Provider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", resp.LLM.Provider, tc.wantProvider)
			}
			if resp.LLM.Configured != tc.wantConfigured {
				t.Errorf("configured = %v, want %v", resp.LLM.Configured, tc.wantConfigured)
			}
			if resp.LLM.AskEnabled != tc.wantAskEnabled {
				t.Errorf("ask_enabled = %v, want %v", resp.LLM.AskEnabled, tc.wantAskEnabled)
			}
		})
	}
}
