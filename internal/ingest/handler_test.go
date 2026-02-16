package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
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

	for _, key := range []string{"window", "total_requests", "total_failures", "error_rate", "top_errors", "recent_traces"} {
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
