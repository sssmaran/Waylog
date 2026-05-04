package otel

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func testV2Ingest(t *testing.T) *ingestv2.Handler {
	t.Helper()
	h, err := ingestv2.New(ingestv2.Config{
		Dedup: ingestv2.NewDedup(ingestv2.DefaultDedupCapacity, nil),
		WAL:   &fakeWAL{},
		Index: ingestv2.NewRecentIndex(nil),
	})
	if err != nil {
		t.Fatalf("ingestv2.New: %v", err)
	}
	return h
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func validOTLPRequest() *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				strAttr("service.name", "test-svc"),
				strAttr("deployment.environment", "prod"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99},
					SpanId:            []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11},
					Name:              "test-op",
					StartTimeUnixNano: 1000000000,
					EndTimeUnixNano:   1050000000,
					Attributes: []*commonpb.KeyValue{
						strAttr("http.request.method", "GET"),
						strAttr("http.route", "/test"),
						intAttr("http.response.status_code", 200),
					},
					Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				}},
			}},
		}},
	}
}

func postOTLP(handler http.Handler, body []byte, contentType, contentEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/otlp/v1/traces", bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentEncoding != "" {
		req.Header.Set("Content-Encoding", contentEncoding)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestHandler_HappyPath(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	body, _ := proto.Marshal(validOTLPRequest())
	rr := postOTLP(h, body, "application/x-protobuf", "")
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Errorf("failed to unmarshal response: %v", err)
	}
	if resp.PartialSuccess != nil && resp.PartialSuccess.RejectedSpans != 0 {
		t.Errorf("expected no partial success, got %+v", resp.PartialSuccess)
	}
}

func TestHandler_GzipCompressed(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	body, _ := proto.Marshal(validOTLPRequest())
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write(body)
	_ = gw.Close()
	rr := postOTLP(h, buf.Bytes(), "application/x-protobuf", "gzip")
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_ContentTypeWithParams(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	body, _ := proto.Marshal(validOTLPRequest())
	rr := postOTLP(h, body, "application/x-protobuf; charset=utf-8", "")
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestHandler_WrongContentType(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	rr := postOTLP(h, []byte("{}"), "application/json", "")
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rr.Code)
	}
}

func TestHandler_UnsupportedContentEncoding(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	body, _ := proto.Marshal(validOTLPRequest())
	rr := postOTLP(h, body, "application/x-protobuf", "deflate")
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rr.Code)
	}
}

func TestHandler_WrongMethod(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	req := httptest.NewRequest(http.MethodGet, "/v1/otlp/v1/traces", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestHandler_MalformedProtobuf(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	rr := postOTLP(h, []byte("not protobuf"), "application/x-protobuf", "")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHandler_BodyTooLarge(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 10)
	body, _ := proto.Marshal(validOTLPRequest())
	rr := postOTLP(h, body, "application/x-protobuf", "")
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_EmptyRequest(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	body, _ := proto.Marshal(&coltracepb.ExportTraceServiceRequest{})
	rr := postOTLP(h, body, "application/x-protobuf", "")
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestHandler_MissingV2IngestReturns503ForConvertedSpans(t *testing.T) {
	h := NewHandler(nil, nil, 1<<20)
	body, _ := proto.Marshal(validOTLPRequest())
	rr := postOTLP(h, body, "application/x-protobuf", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandler_FutureTimestampDropped(t *testing.T) {
	h := NewHandler(testV2Ingest(t), nil, 1<<20)
	req := validOTLPRequest()
	// Stamp the span 10 minutes in the future — should be dropped with
	// partial_success rather than skewing recent traces / overview.
	future := uint64(time.Now().Add(10 * time.Minute).UnixNano())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.StartTimeUnixNano = future
	span.EndTimeUnixNano = future + 50_000_000

	body, _ := proto.Marshal(req)
	rr := postOTLP(h, body, "application/x-protobuf", "")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PartialSuccess == nil {
		t.Fatal("expected partial_success for future-dated span")
	}
	if resp.PartialSuccess.RejectedSpans != 1 {
		t.Errorf("rejected_spans = %d, want 1", resp.PartialSuccess.RejectedSpans)
	}
	if !bytes.Contains([]byte(resp.PartialSuccess.ErrorMessage), []byte("future_timestamp")) {
		t.Errorf("error_message missing future_timestamp reason: %q", resp.PartialSuccess.ErrorMessage)
	}
}

type fakeWAL struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *fakeWAL) WriteRaw(line []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), line...))
	return nil
}
