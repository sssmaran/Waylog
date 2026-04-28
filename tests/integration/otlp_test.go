package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	otelhttp "github.com/sssmaran/WaylogCLI/internal/otel"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpStrAttr is a small constructor for OTLP string attributes.
func otlpStrAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// resourceSpansFor builds a single ResourceSpans for one service with one span.
func resourceSpansFor(service string, traceID, spanID, parentSpanID []byte, name string, startNs, endNs uint64) *tracepb.ResourceSpans {
	span := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		Name:              name,
		StartTimeUnixNano: startNs,
		EndTimeUnixNano:   endNs,
		Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
	}
	if len(parentSpanID) > 0 {
		span.ParentSpanId = parentSpanID
	}
	return &tracepb.ResourceSpans{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			otlpStrAttr("service.name", service),
			otlpStrAttr("deployment.environment", "prod"),
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Spans: []*tracepb.Span{span},
		}},
	}
}

// newOTLPHandler builds an OTLP handler that shares store/builder/sampler/etc.
// with the provided integration server, mirroring what cmd/ingest/main.go does.
func newOTLPHandler(t *testing.T, srv *integrationServer, store *graphstore.Store) *otelhttp.Handler {
	t.Helper()
	// Use a 100% sampler so the test is deterministic regardless of the
	// HAPPY_SAMPLE_RATE_PCT env default the integration server picks up.
	// Notifier is left nil — the integration server isn't running an SSE hub.
	pipe := ingest.NewPipeline(ingest.PipelineConfig{
		Store:     store,
		Builder:   srv.Builder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 100}),
		EventLog:  srv.EventLog,
		Counters:  srv.Counters(),
		Accepted:  srv.AcceptedPtr(),
		Validator: ingest.OTLPValidator,
	})
	return otelhttp.NewHandler(pipe, nil, 1<<20)
}

// TestOTLP_EndToEnd posts a 3-service OTLP trace and verifies it appears
// in /v1/traces/recent and that /v1/capabilities advertises otlp.http_traces.
func TestOTLP_EndToEnd(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	srv.SetOTLPEnabled(true)
	handler := newOTLPHandler(t, srv, srv.Store())

	traceID := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	gatewaySpan := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	checkoutSpan := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	paymentSpan := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			resourceSpansFor("api-gateway", traceID, gatewaySpan, nil, "GET /checkout", 1_000_000_000, 1_080_000_000),
			resourceSpansFor("checkout", traceID, checkoutSpan, gatewaySpan, "checkout-op", 1_010_000_000, 1_070_000_000),
			resourceSpansFor("payment", traceID, paymentSpan, checkoutSpan, "payment-op", 1_020_000_000, 1_060_000_000),
		},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/traces", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httpReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("OTLP POST: expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PartialSuccess != nil && resp.PartialSuccess.RejectedSpans != 0 {
		t.Fatalf("unexpected partial success: rejected=%d msg=%s",
			resp.PartialSuccess.RejectedSpans, resp.PartialSuccess.ErrorMessage)
	}

	// Verify trace surfaced in /v1/traces/recent.
	rw := httpGET(t, srv.RecentTraces, "/v1/traces/recent?limit=10")
	if rw.Code != http.StatusOK {
		t.Fatalf("recent traces: expected 200, got %d", rw.Code)
	}
	var recentResp struct {
		Traces []struct {
			TraceID string `json:"trace_id"`
			Service string `json:"service"`
		} `json:"traces"`
	}
	decodeJSON(t, rw, &recentResp)
	wantTraceID := "aabbccddeeff00112233445566778899"
	found := false
	for _, tr := range recentResp.Traces {
		if tr.TraceID == wantTraceID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected trace %s in recent traces, got %+v", wantTraceID, recentResp.Traces)
	}

	// Verify /v1/capabilities advertises OTLP enabled.
	cw := httpGET(t, srv.Capabilities, "/v1/capabilities")
	if cw.Code != http.StatusOK {
		t.Fatalf("capabilities: expected 200, got %d", cw.Code)
	}
	var caps map[string]any
	decodeJSON(t, cw, &caps)
	otlp, ok := caps["otlp"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing otlp section: %v", caps)
	}
	if enabled, _ := otlp["http_traces"].(bool); !enabled {
		t.Errorf("expected otlp.http_traces=true, got %v", otlp["http_traces"])
	}
}

// silence unused-import for json when no other test in the file uses it
// (decodeJSON in helpers_test.go re-exports usage, but keep this file
// self-evident for future readers).
var _ = json.Marshal
