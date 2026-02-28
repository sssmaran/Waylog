package ingest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

const testTrace = "aaaa0000bbbb1111cccc2222dddd3333"

func makeTestServer() *Server {
	st := graphstore.NewStore()
	b := build.NewBuilder()

	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(45),
			testutil.WithTimestamp(time.Now().Add(-2*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithService("checkout"),
			testutil.WithCallerService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(32),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("3333333333333333"),
			testutil.WithParentSpanID("2222222222222222"),
			testutil.WithService("payment"),
			testutil.WithCallerService("checkout"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "payment failed"),
			testutil.WithLatency(12),
			testutil.WithTimestamp(time.Now().Add(-30*time.Second)),
		),
	}

	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	return &Server{store: st, builder: b}
}

func TestTraceStory_Success(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/story?trace_id="+testTrace, nil)
	w := httptest.NewRecorder()
	srv.TraceStory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, ok := resp["story"]; !ok {
		t.Fatal("missing 'story' key")
	}
	if _, ok := resp["context"]; !ok {
		t.Fatal("missing 'context' key")
	}

	var story struct {
		TraceID  string `json:"trace_id"`
		Chain    []any  `json:"chain"`
		HopCount int    `json:"hop_count"`
	}
	if err := json.Unmarshal(resp["story"], &story); err != nil {
		t.Fatalf("invalid story json: %v", err)
	}
	if story.TraceID != testTrace {
		t.Errorf("trace_id = %q, want %q", story.TraceID, testTrace)
	}
	if story.HopCount != 3 {
		t.Errorf("hop_count = %d, want 3", story.HopCount)
	}
}

func TestTraceStory_NotFound(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/story?trace_id=00000000000000000000000000000000", nil)
	w := httptest.NewRecorder()
	srv.TraceStory(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestTraceStory_MissingParam(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/story", nil)
	w := httptest.NewRecorder()
	srv.TraceStory(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestRecentTraces_Ordering(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/recent?limit=10", nil)
	w := httptest.NewRecorder()
	srv.RecentTraces(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []traceEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one trace entry")
	}

	// Verify descending order by timestamp
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.After(entries[i-1].Timestamp) {
			t.Errorf("entries not sorted desc: [%d].Timestamp > [%d].Timestamp", i, i-1)
		}
	}
}

func TestRecentTraces_Limit(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/recent?limit=1", nil)
	w := httptest.NewRecorder()
	srv.RecentTraces(w, req)

	var entries []traceEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(entries) > 1 {
		t.Errorf("expected at most 1 entry, got %d", len(entries))
	}
}

func TestRecentTraces_FailuresOnlyAndFailureSource(t *testing.T) {
	srv := makeTestServerMixed()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/recent?limit=10&failures_only=true", nil)
	w := httptest.NewRecorder()
	srv.RecentTraces(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var entries []traceEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one failed trace entry")
	}
	for _, e := range entries {
		if e.Success {
			t.Fatalf("expected only failed traces, got success trace %s", e.TraceID)
		}
		if e.FailureService == "" {
			t.Fatalf("expected failure_service for failed trace %s", e.TraceID)
		}
	}
}

func TestOverview_Stats(t *testing.T) {
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/overview?window=10m", nil)
	w := httptest.NewRecorder()
	srv.Overview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	for _, key := range []string{"window", "total_requests", "total_failures", "error_rate", "p50", "p95", "p99", "sampled", "top_errors", "recent_traces"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing key %q in overview response", key)
		}
	}

	totalReqs := int(resp["total_requests"].(float64))
	if totalReqs < 1 {
		t.Errorf("expected total_requests >= 1, got %d", totalReqs)
	}
}

func TestCORSWrap(t *testing.T) {
	handler := CORSWrap("http://localhost:3000", func(w http.ResponseWriter, r *http.Request) {
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

const successTrace = "bbbb0000cccc1111dddd2222eeee3333"

func makeTestServerMixed() *Server {
	st := graphstore.NewStore()
	b := build.NewBuilder()

	events := []event.WideEvent{
		// Trace 1: 3-hop failure (gateway->checkout->payment fails)
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(502),
			testutil.WithLatency(45),
			testutil.WithTimestamp(time.Now().Add(-2*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithService("checkout"),
			testutil.WithCallerService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(32),
			testutil.WithTimestamp(time.Now().Add(-2*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(testTrace),
			testutil.WithSpanID("3333333333333333"),
			testutil.WithParentSpanID("2222222222222222"),
			testutil.WithService("payment"),
			testutil.WithCallerService("checkout"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "payment failed"),
			testutil.WithLatency(12),
			testutil.WithTimestamp(time.Now().Add(-2*time.Minute)),
		),
		// Trace 2: 3-hop success (all 200)
		testutil.MakeEvent(
			testutil.WithTraceID(successTrace),
			testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(40),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(successTrace),
			testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithParentSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithService("checkout"),
			testutil.WithCallerService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(25),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(successTrace),
			testutil.WithSpanID("cccccccccccccccc"),
			testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithService("payment"),
			testutil.WithCallerService("checkout"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(10),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
	}

	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	return &Server{store: st, builder: b}
}

func TestOverview_MixedSuccessAndFailure(t *testing.T) {
	srv := makeTestServerMixed()

	req := httptest.NewRequest(http.MethodGet, "/v1/overview?window=10m", nil)
	w := httptest.NewRecorder()
	srv.Overview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	totalReqs := int(resp["total_requests"].(float64))
	totalFails := int(resp["total_failures"].(float64))
	errorRate := resp["error_rate"].(float64)

	if totalReqs != 2 {
		t.Errorf("total_requests = %d, want 2", totalReqs)
	}
	if totalFails != 1 {
		t.Errorf("total_failures = %d, want 1", totalFails)
	}
	if errorRate != 50.0 {
		t.Errorf("error_rate = %.1f, want 50.0", errorRate)
	}
}

func TestOverview_TopErrors_UniquePerFailedRequest(t *testing.T) {
	st := graphstore.NewStore()
	b := build.NewBuilder()
	traceID := "cccc0000dddd1111eeee2222ffff3333"

	events := []event.WideEvent{
		// First failure in request lifecycle.
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "payment failed"),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		// Later propagated failure on gateway for the same request.
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(502),
			testutil.WithError("GW_DOWNSTREAM", "downstream checkout failed"),
			testutil.WithTimestamp(time.Now().Add(-30*time.Second)),
		),
	}

	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	srv := &Server{store: st, builder: b}
	req := httptest.NewRequest(http.MethodGet, "/v1/overview?window=10m", nil)
	w := httptest.NewRecorder()
	srv.Overview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		TotalFailures int `json:"total_failures"`
		TopErrors     []struct {
			Code  string `json:"code"`
			Count int    `json:"count"`
		} `json:"top_errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp.TotalFailures != 1 {
		t.Fatalf("total_failures = %d, want 1", resp.TotalFailures)
	}
	if len(resp.TopErrors) != 1 {
		t.Fatalf("top_errors len = %d, want 1 (one primary code per failed request)", len(resp.TopErrors))
	}
	if resp.TopErrors[0].Code != "PMT_502" {
		t.Fatalf("top_errors[0].code = %q, want PMT_502", resp.TopErrors[0].Code)
	}
	if resp.TopErrors[0].Count != 1 {
		t.Fatalf("top_errors[0].count = %d, want 1", resp.TopErrors[0].Count)
	}
}

func TestTraceStory_SuccessTrace(t *testing.T) {
	srv := makeTestServerMixed()

	req := httptest.NewRequest(http.MethodGet, "/v1/traces/story?trace_id="+successTrace, nil)
	w := httptest.NewRecorder()
	srv.TraceStory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Story struct {
			TraceID      string `json:"trace_id"`
			Success      bool   `json:"success"`
			HopCount     int    `json:"hop_count"`
			FirstFailHop *any   `json:"first_fail_hop"`
		} `json:"story"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	if resp.Story.TraceID != successTrace {
		t.Errorf("trace_id = %q, want %q", resp.Story.TraceID, successTrace)
	}
	if !resp.Story.Success {
		t.Error("expected success=true for all-200 trace")
	}
	if resp.Story.HopCount != 3 {
		t.Errorf("hop_count = %d, want 3", resp.Story.HopCount)
	}
	if resp.Story.FirstFailHop != nil {
		t.Error("expected first_fail_hop to be nil for success trace")
	}
}

func TestReadEndpoints_NoStore(t *testing.T) {
	srv := NewServer(ServerConfig{})

	t.Run("TraceStory", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/traces/story?trace_id="+testTrace, nil)
		w := httptest.NewRecorder()
		srv.TraceStory(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})

	t.Run("RecentTraces", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/traces/recent", nil)
		w := httptest.NewRecorder()
		srv.RecentTraces(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})

	t.Run("Overview", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/overview", nil)
		w := httptest.NewRecorder()
		srv.Overview(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
		}
	})
}

func TestAPIKeyMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := APIKeyMiddleware("test-secret", inner)

	t.Run("valid Bearer token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
		req.Header.Set("Authorization", "Bearer test-secret")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("valid X-API-Key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
		req.Header.Set("X-API-Key", "test-secret")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
		req.Header.Set("Authorization", "Bearer wrong-key")
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})
}

func TestEvents_BodyTooLarge(t *testing.T) {
	srv := NewServer(ServerConfig{
		Store:        graphstore.NewStore(),
		MaxBodyBytes: 64,
	})

	largeJSON := `{"schema_version":"1.0","event_name":"test.request","padding":"` + strings.Repeat("a", 100) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(largeJSON))
	w := httptest.NewRecorder()
	srv.Events(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", w.Code)
	}
}

func TestEvents_DefaultMaxBody(t *testing.T) {
	srv := NewServer(ServerConfig{
		Store: graphstore.NewStore(),
	})
	if srv.maxBodyBytes != 1<<20 {
		t.Errorf("expected default 1MB, got %d", srv.maxBodyBytes)
	}
}

func TestValidate_ValidEvent(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
	body := `{"schema_version":"1.0","event_name":"test.request","timestamp":"2026-02-17T10:00:00Z","user":{"id":"u1"},"request":{"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"},"system":{"service":"test","env":"prod"},"outcome":{"success":true,"status_code":200,"kind":"http"},"metrics":{"latency_ms":10}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/validate", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Validate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true, got %v", resp["valid"])
	}
}

func TestValidate_InvalidEvent(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
	body := `{"schema_version":"1.0","event_name":"test.request","timestamp":"2026-02-17T10:00:00Z","user":{"id":""},"request":{"trace_id":"aaa"},"system":{"service":"test","env":"prod"},"outcome":{"success":true,"status_code":200,"kind":"http"},"metrics":{"latency_ms":10}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/validate", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Validate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false, got %v", resp["valid"])
	}
}

func TestEventSearch_NoFilter(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore(), EventLogDir: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/v1/events/search", nil)
	w := httptest.NewRecorder()
	srv.EventSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for no filters, got %d", w.Code)
	}
}

func TestEventSearch_NoEventLog(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})

	req := httptest.NewRequest(http.MethodGet, "/v1/events/search?service=x", nil)
	w := httptest.NewRecorder()
	srv.EventSearch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestEventSearch_WithResults(t *testing.T) {
	dir := t.TempDir()

	// Write test events directly
	w2, err := newTestEventLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := testutil.MakeEvent(
		testutil.WithTraceID(testTrace),
		testutil.WithService("checkout"),
		testutil.WithStatusCode(200),
	)
	if err := w2.Write(&ev, true); err != nil {
		t.Fatal(err)
	}
	w2.Close()

	srv := NewServer(ServerConfig{Store: graphstore.NewStore(), EventLogDir: dir})

	req := httptest.NewRequest(http.MethodGet, "/v1/events/search?service=checkout&limit=5", nil)
	rec := httptest.NewRecorder()
	srv.EventSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []event.WideEvent `json:"events"`
		Count  int               `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if resp.Count != 1 {
		t.Errorf("expected count=1, got %d", resp.Count)
	}
	if len(resp.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(resp.Events))
	}
}

func newTestEventLog(dir string) (*eventlog.Writer, error) {
	return eventlog.New(dir)
}

func TestValidate_BadJSON(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
	req := httptest.NewRequest(http.MethodPost, "/v1/events/validate", strings.NewReader("{bad"))
	w := httptest.NewRecorder()
	srv.Validate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestValidate_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
	req := httptest.NewRequest(http.MethodGet, "/v1/events/validate", nil)
	w := httptest.NewRecorder()
	srv.Validate(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestEventSearch_BadStartReturns400(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore(), EventLogDir: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/v1/events/search?service=x&start=garbage", nil)
	w := httptest.NewRecorder()
	srv.EventSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad start, got %d", w.Code)
	}
}

func TestEventSearch_BadEndReturns400(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore(), EventLogDir: t.TempDir()})

	req := httptest.NewRequest(http.MethodGet, "/v1/events/search?service=x&end=not-a-date", nil)
	w := httptest.NewRecorder()
	srv.EventSearch(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad end, got %d", w.Code)
	}
}

func TestLivez(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
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
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	srv.Readyz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestReadyz_Ready(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
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
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
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
	for _, key := range []string{"status", "uptime", "ready", "store", "event_log", "replay"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	storeInfo := resp["store"].(map[string]any)
	if storeInfo["configured"] != true {
		t.Errorf("store.configured = %v, want true", storeInfo["configured"])
	}
	replayInfo := resp["replay"].(map[string]any)
	if replayInfo["status"] != "none" {
		t.Errorf("replay.status = %q, want 'none'", replayInfo["status"])
	}
}

func TestHealth_Degraded(t *testing.T) {
	srv := NewServer(ServerConfig{})

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
	if resp["status"] != "degraded" {
		t.Errorf("status = %q, want 'degraded'", resp["status"])
	}
}

func TestHealth_ReplaySuccess(t *testing.T) {
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
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
	srv := NewServer(ServerConfig{Store: graphstore.NewStore()})
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

func TestOverview_ErrorRateFromPresamplingCounters(t *testing.T) {
	srv := NewServer(ServerConfig{
		Store:   graphstore.NewStore(),
		Sampler: keepAllSampler(),
	})

	// Send 4 events through the handler: 3 success + 1 error.
	makeBody := func(traceID string, success bool, code int, errCode string) string {
		ev := testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithService("svc"),
			testutil.WithStatusCode(code),
		)
		if !success {
			ev.Outcome.Success = false
			ev.Error = &event.ErrorContext{Code: errCode, Message: "fail"}
			ev.EventName = "svc.error"
		}
		b, _ := json.Marshal(ev)
		return string(b)
	}

	for _, body := range []string{
		makeBody("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", true, 200, ""),
		makeBody("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2", true, 200, ""),
		makeBody("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3", true, 200, ""),
		makeBody("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa4", false, 500, "ERR_X"),
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
		w := httptest.NewRecorder()
		srv.Events(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
		}
	}

	// Overview should use pre-sampling counters: 1/4 = 25%.
	req := httptest.NewRequest(http.MethodGet, "/v1/overview?window=10m", nil)
	w := httptest.NewRecorder()
	srv.Overview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	errorRate := resp["error_rate"].(float64)
	if errorRate != 25.0 {
		t.Errorf("error_rate = %.1f, want 25.0 (from pre-sampling counters)", errorRate)
	}
}

func keepAllSampler() *sampler.Sampler {
	return sampler.New(sampler.Config{HappySampleRatePct: 100})
}

func TestEvents_MetricsIncremented(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	srv := NewServer(ServerConfig{
		Store:   graphstore.NewStore(),
		Metrics: m,
		Sampler: keepAllSampler(),
	})

	body := `{"schema_version":"1.0","event_name":"test.request","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","user":{"id":"u1"},"request":{"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"},"system":{"service":"test","env":"prod"},"outcome":{"success":true,"status_code":200,"kind":"http"},"metrics":{"latency_ms":10}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Events(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	fm := gatherMap(families)

	if v := counterValue(fm["waylog_events_accepted_total"]); v < 1 {
		t.Errorf("events_accepted_total = %v, want >= 1", v)
	}
	if v := histogramCount(fm["waylog_ingest_latency_seconds"]); v < 1 {
		t.Errorf("ingest_latency count = %v, want >= 1", v)
	}
	if v := histogramCount(fm["waylog_merge_latency_seconds"]); v < 1 {
		t.Errorf("merge_latency count = %v, want >= 1", v)
	}
}

func TestEvents_RejectedMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	srv := NewServer(ServerConfig{
		Store:   graphstore.NewStore(),
		Metrics: m,
	})

	// Invalid JSON
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader("{bad"))
	w := httptest.NewRecorder()
	srv.Events(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	fm := gatherMap(families)

	mf := fm["waylog_events_rejected_total"]
	if mf == nil {
		t.Fatal("waylog_events_rejected_total not found")
	}
	found := false
	for _, m := range mf.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "reason" && lp.GetValue() == "validation" {
				if m.GetCounter().GetValue() >= 1 {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("events_rejected_total{reason=validation} not >= 1")
	}
}

func TestEvents_EventlogWriteFailRejects(t *testing.T) {
	dir := t.TempDir()
	el, err := eventlog.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Close the writer so subsequent writes fail.
	el.Close()

	srv := NewServer(ServerConfig{
		Store:   graphstore.NewStore(),
		Sampler: keepAllSampler(),
	})
	srv.EventLog = el

	body := `{"schema_version":"1.0","event_name":"test.request","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","user":{"id":"u1"},"request":{"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"},"system":{"service":"test","env":"prod"},"outcome":{"success":true,"status_code":200,"kind":"http"},"metrics":{"latency_ms":10}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.Events(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when eventlog write fails, got %d", w.Code)
	}
	// Event should NOT have been merged into the store.
	if srv.AcceptedCount() != 0 {
		t.Errorf("accepted = %d, want 0 (event should be rejected)", srv.AcceptedCount())
	}
	// Unsampled counters should NOT have been incremented.
	total, errs := srv.counters.Sum(time.Hour)
	if total != 0 || errs != 0 {
		t.Errorf("counters = (%d, %d), want (0, 0) after WAL failure", total, errs)
	}
}

func TestOverviewTimeseries(t *testing.T) {
	srv := makeTestServer()

	t.Run("success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/overview/timeseries?window=10m&step=5m", nil)
		w := httptest.NewRecorder()
		srv.OverviewTimeseries(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Sampled bool `json:"sampled"`
			Buckets []struct {
				Start     string  `json:"start"`
				End       string  `json:"end"`
				Total     int     `json:"total"`
				Failures  int     `json:"failures"`
				ErrorRate float64 `json:"error_rate"`
				Status2xx int     `json:"status_2xx"`
				Status4xx int     `json:"status_4xx"`
				Status5xx int     `json:"status_5xx"`
				P50       int64   `json:"p50"`
				P95       int64   `json:"p95"`
				P99       int64   `json:"p99"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if len(resp.Buckets) != 2 {
			t.Fatalf("expected 2 buckets, got %d", len(resp.Buckets))
		}
		// Should have at least one request across the buckets.
		totalReqs := 0
		for _, b := range resp.Buckets {
			totalReqs += b.Total
		}
		if totalReqs < 1 {
			t.Errorf("expected at least 1 request across buckets, got %d", totalReqs)
		}
	})

	t.Run("guardrail_window_too_large", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/overview/timeseries?window=48h", nil)
		w := httptest.NewRecorder()
		srv.OverviewTimeseries(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("guardrail_step_too_small", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/overview/timeseries?step=5s", nil)
		w := httptest.NewRecorder()
		srv.OverviewTimeseries(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

func TestRoutes(t *testing.T) {
	st := graphstore.NewStore()
	b := build.NewBuilder()

	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithTraceID("aaaa0000bbbb1111cccc2222dddd0001"),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(40),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID("aaaa0000bbbb1111cccc2222dddd0002"),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(60),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID("aaaa0000bbbb1111cccc2222dddd0003"),
			testutil.WithSpanID("3333333333333333"),
			testutil.WithService("checkout"),
			testutil.WithEventName("checkout.request"),
			testutil.WithStatusCode(502),
			testutil.WithError("CHK_502", "checkout failed"),
			testutil.WithLatency(100),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
	}

	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	srv := &Server{store: st, builder: b}

	req := httptest.NewRequest(http.MethodGet, "/v1/routes?window=10m", nil)
	w := httptest.NewRecorder()
	srv.Routes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Sampled bool `json:"sampled"`
		Routes  []struct {
			Service      string  `json:"service"`
			Route        string  `json:"route"`
			Invocations  int     `json:"invocations"`
			Errors       int     `json:"errors"`
			ErrorRate    float64 `json:"error_rate"`
			Status2xx    int     `json:"status_2xx"`
			Status4xx    int     `json:"status_4xx"`
			Status5xx    int     `json:"status_5xx"`
			P75LatencyMs int64   `json:"p75_latency_ms"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	if len(resp.Routes) < 2 {
		t.Fatalf("expected at least 2 routes, got %d", len(resp.Routes))
	}

	// First route should be api-gateway (2 invocations) sorted by invocations desc.
	if resp.Routes[0].Service != "api-gateway" {
		t.Errorf("first route service = %q, want api-gateway", resp.Routes[0].Service)
	}
	if resp.Routes[0].Invocations != 2 {
		t.Errorf("api-gateway invocations = %d, want 2", resp.Routes[0].Invocations)
	}
	if resp.Routes[0].Status2xx != 2 {
		t.Errorf("api-gateway status_2xx = %d, want 2", resp.Routes[0].Status2xx)
	}

	// Second route: checkout with 1 error.
	found := false
	for _, r := range resp.Routes {
		if r.Service == "checkout" {
			found = true
			if r.Invocations != 1 {
				t.Errorf("checkout invocations = %d, want 1", r.Invocations)
			}
			if r.Errors != 1 {
				t.Errorf("checkout errors = %d, want 1", r.Errors)
			}
			if r.ErrorRate != 100.0 {
				t.Errorf("checkout error_rate = %.1f, want 100.0", r.ErrorRate)
			}
			if r.Status5xx != 1 {
				t.Errorf("checkout status_5xx = %d, want 1", r.Status5xx)
			}
		}
	}
	if !found {
		t.Error("checkout route not found")
	}
}

func TestRoutes_FailuresOnly(t *testing.T) {
	st := graphstore.NewStore()
	b := build.NewBuilder()

	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithTraceID("aaaa0000bbbb1111cccc2222dddd1010"),
			testutil.WithSpanID("1010101010101010"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(40),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID("aaaa0000bbbb1111cccc2222dddd1011"),
			testutil.WithSpanID("1111111111111011"),
			testutil.WithService("checkout"),
			testutil.WithEventName("checkout.request"),
			testutil.WithStatusCode(502),
			testutil.WithError("CHK_502", "checkout failed"),
			testutil.WithLatency(80),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		),
	}
	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	srv := &Server{store: st, builder: b}
	req := httptest.NewRequest(http.MethodGet, "/v1/routes?window=10m&failures_only=true", nil)
	w := httptest.NewRecorder()
	srv.Routes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Routes []struct {
			Service string `json:"service"`
			Route   string `json:"route"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(resp.Routes) != 1 {
		t.Fatalf("expected exactly 1 failed route, got %d", len(resp.Routes))
	}
	if resp.Routes[0].Service != "checkout" {
		t.Fatalf("service = %q, want checkout", resp.Routes[0].Service)
	}
}

func TestRoutes_RootServiceAttribution(t *testing.T) {
	traceID := "aaaa0000bbbb1111cccc2222dddd9999"

	t.Run("root_arrives_later", func(t *testing.T) {
		st := graphstore.NewStore()
		b := build.NewBuilder()

		// Child span arrives first — service=payment, not the root.
		child := testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithService("payment"),
			testutil.WithEventName("payment.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(10),
			testutil.WithTimestamp(time.Now().Add(-2*time.Minute)),
		)
		st.Merge(b.Build(child))

		// Root span arrives later — service=api-gateway.
		root := testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(50),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		)
		st.Merge(b.Build(root))

		srv := &Server{store: st, builder: b}
		req := httptest.NewRequest(http.MethodGet, "/v1/routes?window=10m", nil)
		w := httptest.NewRecorder()
		srv.Routes(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Routes []struct {
				Service string `json:"service"`
				Route   string `json:"route"`
			} `json:"routes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if len(resp.Routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(resp.Routes))
		}
		if resp.Routes[0].Service != "api-gateway" {
			t.Errorf("service = %q, want api-gateway (root_service)", resp.Routes[0].Service)
		}
	})

	t.Run("root_absent_fallback", func(t *testing.T) {
		st := graphstore.NewStore()
		b := build.NewBuilder()

		// Only a child span — no root span arrives.
		child := testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithService("payment"),
			testutil.WithEventName("payment.request"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(10),
			testutil.WithTimestamp(time.Now().Add(-1*time.Minute)),
		)
		st.Merge(b.Build(child))

		srv := &Server{store: st, builder: b}
		req := httptest.NewRequest(http.MethodGet, "/v1/routes?window=10m", nil)
		w := httptest.NewRecorder()
		srv.Routes(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp struct {
			Routes []struct {
				Service string `json:"service"`
				Route   string `json:"route"`
			} `json:"routes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if len(resp.Routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(resp.Routes))
		}
		// Fallback: derived from event_name prefix.
		if resp.Routes[0].Service != "payment" {
			t.Errorf("service = %q, want payment (event_name fallback)", resp.Routes[0].Service)
		}
	})
}

func TestRoutes_GroupByMethodAndRoute(t *testing.T) {
	st := graphstore.NewStore()
	b := build.NewBuilder()

	now := time.Now().Add(-1 * time.Minute)
	events := []event.WideEvent{
		// Same service + same route template, different methods -> separate groups.
		testutil.MakeEvent(
			testutil.WithTraceID("11110000bbbb1111cccc2222dddd0001"),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithHTTPMethod("GET"),
			testutil.WithRouteTemplate("/users/{id}"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(20),
			testutil.WithTimestamp(now),
		),
		testutil.MakeEvent(
			testutil.WithTraceID("11110000bbbb1111cccc2222dddd0002"),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithHTTPMethod("POST"),
			testutil.WithRouteTemplate("/users/{id}"),
			testutil.WithStatusCode(201),
			testutil.WithLatency(35),
			testutil.WithTimestamp(now),
		),
		// Same service + same method, different route template -> separate groups.
		testutil.MakeEvent(
			testutil.WithTraceID("11110000bbbb1111cccc2222dddd0003"),
			testutil.WithSpanID("3333333333333333"),
			testutil.WithService("api-gateway"),
			testutil.WithEventName("api-gateway.request"),
			testutil.WithHTTPMethod("GET"),
			testutil.WithRouteTemplate("/orders/{id}"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(18),
			testutil.WithTimestamp(now),
		),
		// Legacy event: no method/template -> UNKNOWN + event_name fallback.
		testutil.MakeEvent(
			testutil.WithTraceID("11110000bbbb1111cccc2222dddd0004"),
			testutil.WithSpanID("4444444444444444"),
			testutil.WithService("checkout"),
			testutil.WithEventName("checkout.request"),
			testutil.WithStatusCode(502),
			testutil.WithError("CHK_502", "checkout failed"),
			testutil.WithLatency(90),
			testutil.WithTimestamp(now),
		),
	}

	for _, ev := range events {
		st.Merge(b.Build(ev))
	}

	srv := &Server{store: st, builder: b}
	req := httptest.NewRequest(http.MethodGet, "/v1/routes?window=10m&limit=10", nil)
	w := httptest.NewRecorder()
	srv.Routes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Routes []struct {
			Service       string `json:"service"`
			Method        string `json:"method"`
			RouteTemplate string `json:"route_template"`
			Route         string `json:"route"`
			Invocations   int    `json:"invocations"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(resp.Routes) < 4 {
		t.Fatalf("expected at least 4 grouped routes, got %d", len(resp.Routes))
	}

	seen := map[string]bool{}
	for _, r := range resp.Routes {
		key := r.Service + "|" + r.Method + "|" + r.RouteTemplate
		seen[key] = true
		if r.Route != r.RouteTemplate {
			t.Errorf("route alias mismatch: route=%q route_template=%q", r.Route, r.RouteTemplate)
		}
	}

	if !seen["api-gateway|GET|/users/{id}"] {
		t.Error("missing group api-gateway|GET|/users/{id}")
	}
	if !seen["api-gateway|POST|/users/{id}"] {
		t.Error("missing group api-gateway|POST|/users/{id}")
	}
	if !seen["api-gateway|GET|/orders/{id}"] {
		t.Error("missing group api-gateway|GET|/orders/{id}")
	}
	if !seen["checkout|UNKNOWN|checkout.error"] {
		t.Error("missing legacy fallback group checkout|UNKNOWN|checkout.error")
	}
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

func TestGraphTopology(t *testing.T) {
	// makeTestServer creates: api-gateway -> checkout -> payment (PMT_502)
	// Span nodes have caller_service attrs set for checkout and payment.
	srv := makeTestServer()

	req := httptest.NewRequest(http.MethodGet, "/v1/graph/topology?window=1h", nil)
	w := httptest.NewRecorder()
	srv.GraphTopology(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Nodes []struct {
			Data struct {
				ID          string  `json:"id"`
				Label       string  `json:"label"`
				Type        string  `json:"type"`
				Invocations int     `json:"invocations"`
				Errors      int     `json:"errors"`
				ErrorRate   float64 `json:"error_rate"`
			} `json:"data"`
		} `json:"nodes"`
		Edges []struct {
			Data struct {
				Source string `json:"source"`
				Target string `json:"target"`
				Label  string `json:"label"`
				Count  int    `json:"count"`
			} `json:"data"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	// Should have service nodes (at least api-gateway, checkout, payment)
	if len(resp.Nodes) < 3 {
		t.Errorf("expected at least 3 nodes, got %d", len(resp.Nodes))
	}
	for _, n := range resp.Nodes {
		if n.Data.Type != "service" {
			t.Errorf("node %q has type %q, want service", n.Data.ID, n.Data.Type)
		}
		if n.Data.Label == "" {
			t.Errorf("node %q has empty label", n.Data.ID)
		}
	}

	// Should have edges from span caller_service -> service
	if len(resp.Edges) < 2 {
		t.Errorf("expected at least 2 edges, got %d", len(resp.Edges))
	}
	for _, e := range resp.Edges {
		if e.Data.Source == "" || e.Data.Target == "" {
			t.Errorf("edge has empty source or target")
		}
		if e.Data.Count < 1 {
			t.Errorf("edge %s->%s has count %d, want >= 1", e.Data.Source, e.Data.Target, e.Data.Count)
		}
	}

	// Error attribution: payment (status 502, success=false) should carry the
	// error, NOT the root service api-gateway.
	nodeByID := map[string]struct {
		Invocations int
		Errors      int
		ErrorRate   float64
	}{}
	for _, n := range resp.Nodes {
		nodeByID[n.Data.ID] = struct {
			Invocations int
			Errors      int
			ErrorRate   float64
		}{n.Data.Invocations, n.Data.Errors, n.Data.ErrorRate}
	}
	if pmt, ok := nodeByID["payment"]; !ok {
		t.Error("payment node missing")
	} else if pmt.Errors == 0 {
		t.Errorf("payment errors = 0, want > 0 (failure should be attributed to originating service)")
	}
	// api-gateway's own span succeeded (200) — it must not inherit downstream errors.
	gw, ok := nodeByID["api-gateway"]
	if !ok {
		t.Fatal("api-gateway node missing")
	}
	if gw.Errors != 0 {
		t.Errorf("api-gateway errors = %d, want 0 (downstream failures must not inflate root service)", gw.Errors)
	}
}

func TestGraphTopology_MethodNotAllowed(t *testing.T) {
	srv := makeTestServer()
	req := httptest.NewRequest(http.MethodPost, "/v1/graph/topology", nil)
	w := httptest.NewRecorder()
	srv.GraphTopology(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestGraphTopology_NoStore(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/v1/graph/topology?window=5m", nil)
	w := httptest.NewRecorder()
	srv.GraphTopology(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestGraphTopology_WindowClamped(t *testing.T) {
	srv := makeTestServer()
	// Request a 48h window — should be clamped to 24h and still work.
	req := httptest.NewRequest(http.MethodGet, "/v1/graph/topology?window=48h", nil)
	w := httptest.NewRecorder()
	srv.GraphTopology(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCapabilities_GraphFlag(t *testing.T) {
	tests := []struct {
		name    string
		graphUI bool
		want    bool
	}{
		{"disabled", false, false},
		{"enabled", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{GraphUI: tt.graphUI})

			req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
			w := httptest.NewRecorder()
			srv.Capabilities(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}

			var resp struct {
				Graph bool `json:"graph"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if resp.Graph != tt.want {
				t.Errorf("graph = %v, want %v", resp.Graph, tt.want)
			}
		})
	}
}
