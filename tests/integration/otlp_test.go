package integration

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/ingest"
	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
	otelhttp "github.com/sssmaran/WaylogCLI/internal/otel"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func otlpStrAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func otlpIntAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func resourceSpansFor(service string, span *tracepb.Span) *tracepb.ResourceSpans {
	return &tracepb.ResourceSpans{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			otlpStrAttr("service.name", service),
			otlpStrAttr("deployment.environment", "prod"),
		}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{span}}},
	}
}

type otlpV2Stack struct {
	otlp *otelhttp.Handler
	read *ingestv2.ReadHandler
	caps *ingest.Server
}

func newOTLPV2Stack(t *testing.T) otlpV2Stack {
	t.Helper()
	index := ingestv2.NewRecentIndex(nil)
	v2, err := ingestv2.New(ingestv2.Config{
		Dedup: ingestv2.NewDedup(ingestv2.DefaultDedupCapacity, nil),
		WAL:   &fakeV2WAL{},
		Index: index,
	})
	if err != nil {
		t.Fatalf("ingestv2.New: %v", err)
	}
	return otlpV2Stack{
		otlp: otelhttp.NewHandler(v2, nil, 1<<20),
		read: ingestv2.NewReadHandler(ingestv2.NewReader(index), nil, 24*time.Hour),
		caps: ingest.NewServer(ingest.ServerConfig{OTLPEnabled: true}),
	}
}

// TestOTLP_EndToEnd posts a 4-span OTLP checkout cascade and verifies it
// surfaces through the schema-2.0 read APIs used by CLI/dashboard.
func TestOTLP_EndToEnd(t *testing.T) {
	stack := newOTLPV2Stack(t)
	traceID := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	gatewaySpan := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	checkoutSpan := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	checkoutPaymentSpan := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28}
	paymentSpan := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
	base := uint64(time.Now().Add(-time.Second).UnixNano())

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			resourceSpansFor("api-gateway", httpSpan(traceID, gatewaySpan, nil, "POST /purchase", base, base+90_000_000, 502,
				otlpStrAttr("http.request.method", "POST"),
				otlpStrAttr("http.route", "/purchase"),
			)),
			resourceSpansFor("checkout", httpSpan(traceID, checkoutSpan, gatewaySpan, "POST /checkout", base+10_000_000, base+80_000_000, 502,
				otlpStrAttr("http.request.method", "POST"),
				otlpStrAttr("http.route", "/checkout"),
			)),
			resourceSpansFor("checkout", httpSpan(traceID, checkoutPaymentSpan, checkoutSpan, "POST payment", base+30_000_000, base+70_000_000, 502,
				otlpStrAttr("http.request.method", "POST"),
				otlpStrAttr("http.route", "/charge"),
				otlpStrAttr("waylog.step", "payment.charge"),
				otlpStrAttr("waylog.error_code", "PMT_502"),
				otlpStrAttr("peer.service", "payment"),
			)),
			resourceSpansFor("payment", httpSpan(traceID, paymentSpan, checkoutPaymentSpan, "POST /charge", base+35_000_000, base+65_000_000, 200,
				otlpStrAttr("http.request.method", "POST"),
				otlpStrAttr("http.route", "/charge"),
			)),
		},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/traces", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	rr := httptest.NewRecorder()
	stack.otlp.ServeHTTP(rr, httpReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("OTLP POST: expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PartialSuccess != nil && resp.PartialSuccess.RejectedSpans != 0 {
		t.Fatalf("unexpected partial success: rejected=%d msg=%s", resp.PartialSuccess.RejectedSpans, resp.PartialSuccess.ErrorMessage)
	}

	wantTraceID := "aabbccddeeff00112233445566778899"
	assertErrorsContainPaymentFamily(t, stack.read)
	assertStoryShowsPaymentFailure(t, stack.read, wantTraceID)
	assertBlastShowsPaymentImpact(t, stack.read)
	assertCapabilitiesAdvertiseOTLP(t, stack.caps)
}

func httpSpan(traceID, spanID, parentSpanID []byte, name string, start, end uint64, status int64, attrs ...*commonpb.KeyValue) *tracepb.Span {
	allAttrs := append([]*commonpb.KeyValue{otlpIntAttr("http.response.status_code", status)}, attrs...)
	code := tracepb.Status_STATUS_CODE_OK
	msg := ""
	if status >= 500 {
		code = tracepb.Status_STATUS_CODE_ERROR
		msg = "upstream gateway 5xx"
	}
	span := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		ParentSpanId:      parentSpanID,
		Name:              name,
		Kind:              tracepb.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: start,
		EndTimeUnixNano:   end,
		Attributes:        allAttrs,
		Status:            &tracepb.Status{Code: code, Message: msg},
	}
	for _, attr := range attrs {
		if attr.GetKey() == "peer.service" {
			span.Kind = tracepb.Span_SPAN_KIND_CLIENT
			break
		}
	}
	return span
}

func assertErrorsContainPaymentFamily(t *testing.T, h *ingestv2.ReadHandler) {
	t.Helper()
	rw := httpGET(t, h.Errors, "/v1/errors?window=15m&limit=10")
	if rw.Code != http.StatusOK {
		t.Fatalf("errors: expected 200, got %d body=%s", rw.Code, rw.Body.String())
	}
	var resp apiv2.ErrorsResponse
	decodeJSON(t, rw, &resp)
	for _, row := range resp.Rows {
		if row.ErrorFamily.Service == "checkout" && row.ErrorFamily.Step == "payment.charge" && row.ErrorFamily.ErrorCode == "PMT_502" {
			if row.Count != 1 || row.AffectedTraces != 1 {
				t.Fatalf("payment row counts=%+v", row)
			}
			return
		}
	}
	t.Fatalf("payment family not found in rows: %+v", resp.Rows)
}

func assertStoryShowsPaymentFailure(t *testing.T, h *ingestv2.ReadHandler, traceID string) {
	t.Helper()
	rw := httpGET(t, h.TraceStory, "/v1/traces/story?trace_id="+traceID)
	if rw.Code != http.StatusOK {
		t.Fatalf("trace story: expected 200, got %d body=%s", rw.Code, rw.Body.String())
	}
	var story apiv2.StoryResponse
	decodeJSON(t, rw, &story)
	if story.Status != eventv2.StatusError {
		t.Fatalf("status=%q want error", story.Status)
	}
	if story.Anchor == nil || story.Anchor.Step != "payment.charge" || story.Anchor.ErrorCode != "PMT_502" {
		t.Fatalf("anchor=%+v", story.Anchor)
	}
	if story.Linkage != apiv2.LinkageCausal {
		t.Fatalf("linkage=%q want causal", story.Linkage)
	}
	foundDownstream := false
	for _, downstream := range story.Downstream {
		if downstream.Step == "payment.charge" && downstream.Service == "payment" {
			foundDownstream = true
			break
		}
	}
	if !foundDownstream {
		t.Fatalf("downstream payment not found: %+v", story.Downstream)
	}
}

func assertBlastShowsPaymentImpact(t *testing.T, h *ingestv2.ReadHandler) {
	t.Helper()
	rw := httpGET(t, h.BlastRadius, "/v1/blast_radius?service=checkout&step=payment.charge&error_code=PMT_502&window=15m")
	if rw.Code != http.StatusOK {
		t.Fatalf("blast: expected 200, got %d body=%s", rw.Code, rw.Body.String())
	}
	var blast apiv2.BlastRadiusResponse
	decodeJSON(t, rw, &blast)
	if blast.AffectedRequests != 1 || blast.ViewMode != apiv2.BlastViewSingleFamily {
		t.Fatalf("blast=%+v", blast)
	}
}

func assertCapabilitiesAdvertiseOTLP(t *testing.T, srv *ingest.Server) {
	t.Helper()
	cw := httpGET(t, srv.Capabilities, "/v1/capabilities")
	if cw.Code != http.StatusOK {
		t.Fatalf("capabilities: expected 200, got %d", cw.Code)
	}
	var caps struct {
		OTLP struct {
			HTTPTraces bool `json:"http_traces"`
		} `json:"otlp"`
	}
	decodeJSON(t, cw, &caps)
	if !caps.OTLP.HTTPTraces {
		t.Fatal("expected otlp.http_traces=true")
	}
}

type fakeV2WAL struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *fakeV2WAL) WriteRaw(line []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), line...))
	return nil
}
