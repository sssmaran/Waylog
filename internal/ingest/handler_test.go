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
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

const testTrace = "aaaa0000bbbb1111cccc2222dddd3333"

func makeTestServer() *Server {
	st := graphstore.NewStore()
	ts := tracestore.NewStore()
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
		result := b.BuildResult(ev)
		st.Merge(result.Graph)
		if result.Span != nil {
			ts.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), result.Span)
		}
	}

	return &Server{store: st, traceStore: ts, builder: b}
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

	var resp struct {
		Traces     []traceEntry `json:"traces"`
		TotalCount int          `json:"total_count"`
		NextCursor string       `json:"next_cursor,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	entries := resp.Traces
	if len(entries) == 0 {
		t.Fatal("expected at least one trace entry")
	}
	if resp.TotalCount < len(entries) {
		t.Errorf("total_count %d < returned entries %d", resp.TotalCount, len(entries))
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

	var resp struct {
		Traces     []traceEntry `json:"traces"`
		TotalCount int          `json:"total_count"`
		NextCursor string       `json:"next_cursor,omitempty"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(resp.Traces) > 1 {
		t.Errorf("expected at most 1 entry, got %d", len(resp.Traces))
	}
	if resp.TotalCount > 1 && resp.NextCursor == "" {
		t.Error("expected next_cursor when total_count > limit")
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

	var resp struct {
		Traces     []traceEntry `json:"traces"`
		TotalCount int          `json:"total_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	entries := resp.Traces
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
		V2Reads struct {
			Enabled bool `json:"enabled"`
		} `json:"v2_reads"`
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
	if resp.V2Reads.Enabled {
		t.Errorf("v2_reads.enabled = true, want false")
	}
}

func TestCapabilities_V2ReadsEnabled(t *testing.T) {
	srv := NewServer(ServerConfig{V2ReadsEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	w := httptest.NewRecorder()
	srv.Capabilities(w, req)

	var resp struct {
		V2Reads struct {
			Enabled bool `json:"enabled"`
		} `json:"v2_reads"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !resp.V2Reads.Enabled {
		t.Fatal("v2_reads.enabled = false, want true")
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

func makeTestServerMixed() *Server {
	st := graphstore.NewStore()
	ts := tracestore.NewStore()
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
		result := b.BuildResult(ev)
		st.Merge(result.Graph)
		if result.Span != nil {
			ts.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), result.Span)
		}
	}

	return &Server{store: st, traceStore: ts, builder: b}
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

	// Seed 4 events into the graph and pre-sampling counters: 3 success + 1 error.
	makeEvent := func(traceID string, success bool, code int, errCode string) event.WideEvent {
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
		return ev
	}

	for _, ev := range []event.WideEvent{
		makeEvent("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", true, 200, ""),
		makeEvent("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2", true, 200, ""),
		makeEvent("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3", true, 200, ""),
		makeEvent("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa4", false, 500, "ERR_X"),
	} {
		result := srv.builder.BuildResult(ev)
		srv.store.Merge(result.Graph)
		srv.counters.Inc(!ev.Outcome.Success)
		if result.Span != nil {
			srv.traceStore.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), result.Span)
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

func TestCapabilities_Architecture(t *testing.T) {
	t.Setenv("GRAPH_HOT_WINDOW", "90m")
	t.Setenv("GRAPH_RETENTION", "24h")

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
	traceStore, ok := arch["trace_store"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture.trace_store block: %#v", arch["trace_store"])
	}
	if enabled, ok := traceStore["enabled"].(bool); !ok || !enabled {
		t.Fatalf("architecture.trace_store.enabled = %v, want true", traceStore["enabled"])
	}
	graph, ok := arch["graph"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture.graph block: %#v", arch["graph"])
	}
	nodes, ok := graph["nodes"].([]any)
	if !ok {
		t.Fatalf("architecture.graph.nodes has unexpected type %T", graph["nodes"])
	}
	if len(nodes) != 3 {
		t.Fatalf("architecture.graph.nodes len = %d, want 3", len(nodes))
	}
	if nodes[0] != "request" || nodes[1] != "service" || nodes[2] != "error" {
		t.Fatalf("architecture.graph.nodes = %#v, want [request service error]", nodes)
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

func TestCapabilities_HotWindowFallbackToRetention(t *testing.T) {
	t.Setenv("GRAPH_HOT_WINDOW", "")
	t.Setenv("GRAPH_RETENTION", "2h")

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
	arch, ok := resp["architecture"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture capability block: %#v", resp["architecture"])
	}
	hotWindow, ok := arch["hot_window"].(map[string]any)
	if !ok {
		t.Fatalf("missing architecture.hot_window block: %#v", arch["hot_window"])
	}
	if source, ok := hotWindow["source"].(string); !ok || source != "GRAPH_RETENTION" {
		t.Fatalf("architecture.hot_window.source = %v, want GRAPH_RETENTION", hotWindow["source"])
	}
	if duration, ok := hotWindow["duration"].(string); !ok || duration != "2h0m0s" {
		t.Fatalf("architecture.hot_window.duration = %v, want 2h0m0s", hotWindow["duration"])
	}
	if secs, ok := hotWindow["duration_secs"].(float64); !ok || int64(secs) != 7200 {
		t.Fatalf("architecture.hot_window.duration_secs = %v, want 7200", hotWindow["duration_secs"])
	}
}

// --- Agentic API fix tests ---

func TestAsk_InvalidJSON_EnvelopeError(t *testing.T) {
	srv := &Server{store: graphstore.NewStore(), maxBodyBytes: 1 << 20}
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
	srv := &Server{store: graphstore.NewStore(), maxBodyBytes: 1 << 20, askRegistry: reg}
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
		Handler: func(ctx context.Context, store tools.Store, params json.RawMessage) (any, error) {
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
	srv := &Server{store: graphstore.NewStore(), maxBodyBytes: 1 << 20, askRegistry: reg, planStore: ps}
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
		Handler: func(ctx context.Context, store tools.Store, params json.RawMessage) (any, error) {
			return map[string]string{"ok": "true"}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	srv := &Server{store: graphstore.NewStore(), maxBodyBytes: 1 << 20, askRegistry: reg}
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
		store:        graphstore.NewStore(),
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
		store:        graphstore.NewStore(),
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
		Handler: func(_ context.Context, _ tools.Store, _ json.RawMessage) (any, error) {
			return nil, fmt.Errorf("trace not found")
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		store:        graphstore.NewStore(),
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
	srv := &Server{store: graphstore.NewStore(), maxBodyBytes: 1 << 20, dedupCache: dc}

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
	srv := &Server{metrics: m, store: graphstore.NewStore(), maxBodyBytes: 1 << 20}

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
		store:        graphstore.NewStore(),
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

func TestTopology_ReturnsNodesAndEdges(t *testing.T) {
	srv := makeTestServer()
	req := httptest.NewRequest("GET", "/v1/topology?window=1h", nil)
	w := httptest.NewRecorder()
	srv.Topology(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data should be object, got %T", resp.Data)
	}
	if _, ok := data["nodes"]; !ok {
		t.Fatal("missing nodes field")
	}
	if _, ok := data["edges"]; !ok {
		t.Fatal("missing edges field")
	}
}

func TestBlastRadiusEndpoint_RequiresErrorCode(t *testing.T) {
	srv := makeTestServer()
	req := httptest.NewRequest("GET", "/v1/blast_radius", nil)
	w := httptest.NewRecorder()
	srv.BlastRadius(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestBlastRadiusEndpoint_ReturnsResult(t *testing.T) {
	srv := makeTestServer()
	req := httptest.NewRequest("GET", "/v1/blast_radius?error_code=DB_TIMEOUT&window=1h", nil)
	w := httptest.NewRecorder()
	srv.BlastRadius(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOverview_IncludesLatestFailedTraceID(t *testing.T) {
	srv := makeTestServer()
	req := httptest.NewRequest("GET", "/v1/overview?window=1h", nil)
	w := httptest.NewRecorder()
	srv.Overview(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["latest_failed_trace_id"]; !ok {
		t.Fatal("overview response missing latest_failed_trace_id field")
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
