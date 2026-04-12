package convert

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func strAttr(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: val}}}
}

func intAttr(key string, val int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: val}}}
}

func minimalRequest(serviceName string, traceID, spanID []byte) *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					strAttr("service.name", serviceName),
					strAttr("deployment.environment", "prod"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           traceID,
					SpanId:            spanID,
					Name:              "GET /api/users",
					StartTimeUnixNano: 1000000000,
					EndTimeUnixNano:   1050000000,
					Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				}},
			}},
		}},
	}
}

func hexTraceID() []byte {
	return []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
}

func hexSpanID() []byte {
	return []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
}

func TestSpansToEvents_MinimalSpan(t *testing.T) {
	req := minimalRequest("checkout", hexTraceID(), hexSpanID())
	res := SpansToEvents(req)

	if res.Dropped != 0 {
		t.Errorf("expected 0 dropped, got %d", res.Dropped)
	}
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	ev := res.Events[0]

	if ev.System.Service != "checkout" {
		t.Errorf("service = %q, want checkout", ev.System.Service)
	}
	if ev.System.Env != "prod" {
		t.Errorf("env = %q, want prod", ev.System.Env)
	}
	if ev.Request.TraceID != "aabbccddeeff00112233445566778899" {
		t.Errorf("trace_id = %q", ev.Request.TraceID)
	}
	if ev.Request.SpanID != "aabbccddeeff0011" {
		t.Errorf("span_id = %q", ev.Request.SpanID)
	}
	if ev.Outcome.StatusCode != 200 {
		t.Errorf("status_code = %d, want 200", ev.Outcome.StatusCode)
	}
	if !ev.Outcome.Success {
		t.Error("expected success=true")
	}
	if ev.EventName != "checkout.request" {
		t.Errorf("event_name = %q, want checkout.request", ev.EventName)
	}
	if ev.Metrics.LatencyMs != 50 {
		t.Errorf("latency_ms = %d, want 50", ev.Metrics.LatencyMs)
	}
	if ev.User.ID != "" {
		t.Errorf("expected empty user.id, got %q", ev.User.ID)
	}
	if ev.SchemaVersion != event.SchemaVersion {
		t.Errorf("schema_version = %q", ev.SchemaVersion)
	}
	if ev.Outcome.Kind != "internal" {
		t.Errorf("kind = %q, want internal", ev.Outcome.Kind)
	}
}

func TestSpansToEvents_MissingServiceName(t *testing.T) {
	req := minimalRequest("", hexTraceID(), hexSpanID())
	// Keep only the env attribute.
	req.ResourceSpans[0].Resource.Attributes = req.ResourceSpans[0].Resource.Attributes[1:]
	res := SpansToEvents(req)
	if res.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %d", res.Dropped)
	}
	if len(res.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(res.Events))
	}
	if len(res.Drops) != 1 || res.Drops[0].Reason != DropMissingService {
		t.Errorf("expected DropMissingService, got %v", res.Drops)
	}
}

func TestSpansToEvents_MissingTraceID(t *testing.T) {
	req := minimalRequest("svc", nil, hexSpanID())
	res := SpansToEvents(req)
	if res.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %d", res.Dropped)
	}
	if len(res.Drops) != 1 || res.Drops[0].Reason != DropMissingTraceID {
		t.Errorf("expected DropMissingTraceID, got %v", res.Drops)
	}
}

func TestSpansToEvents_MissingSpanID(t *testing.T) {
	req := minimalRequest("svc", hexTraceID(), nil)
	res := SpansToEvents(req)
	if res.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %d", res.Dropped)
	}
	if len(res.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(res.Events))
	}
	if len(res.Drops) != 1 || res.Drops[0].Reason != DropInvalidSpan {
		t.Errorf("expected DropInvalidSpan, got %v", res.Drops)
	}
}

func TestSpansToEvents_ZeroSpanID(t *testing.T) {
	zeros := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	req := minimalRequest("svc", hexTraceID(), zeros)
	res := SpansToEvents(req)
	if len(res.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(res.Events))
	}
	if len(res.Drops) != 1 || res.Drops[0].Reason != DropInvalidSpan {
		t.Errorf("expected DropInvalidSpan, got %v", res.Drops)
	}
}

func TestSpansToEvents_ZeroSpans(t *testing.T) {
	req := &coltracepb.ExportTraceServiceRequest{}
	res := SpansToEvents(req)
	if len(res.Events) != 0 || res.Dropped != 0 {
		t.Errorf("expected empty result for zero spans")
	}
}

func TestSpansToEvents_HTTPStatus500(t *testing.T) {
	req := minimalRequest("payment", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{
		intAttr("http.response.status_code", 500),
		strAttr("http.request.method", "POST"),
		strAttr("http.route", "/api/pay"),
	}
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "internal error"}
	span.Events = []*tracepb.Span_Event{{
		Name: "exception",
		Attributes: []*commonpb.KeyValue{
			strAttr("exception.type", "PaymentError"),
			strAttr("exception.message", "insufficient funds"),
		},
	}}

	res := SpansToEvents(req)
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	ev := res.Events[0]
	if ev.Outcome.Success {
		t.Error("expected failure")
	}
	if ev.Outcome.StatusCode != 500 {
		t.Errorf("status_code = %d, want 500", ev.Outcome.StatusCode)
	}
	if ev.EventName != "payment.error" {
		t.Errorf("event_name = %q, want payment.error", ev.EventName)
	}
	if ev.Error == nil || ev.Error.Code != "PaymentError" {
		t.Errorf("error.code = %v", ev.Error)
	}
	if ev.Error.Message != "insufficient funds" {
		t.Errorf("error.message = %q", ev.Error.Message)
	}
	if ev.Request.HTTPMethod != "POST" {
		t.Errorf("http_method = %q", ev.Request.HTTPMethod)
	}
	if ev.Request.RouteTemplate != "/api/pay" {
		t.Errorf("route_template = %q", ev.Request.RouteTemplate)
	}
	if ev.Outcome.Kind != "http" {
		t.Errorf("kind = %q, want http", ev.Outcome.Kind)
	}
}

func TestSpansToEvents_HTTP404IsSuccess(t *testing.T) {
	req := minimalRequest("api", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{intAttr("http.response.status_code", 404)}
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET}
	res := SpansToEvents(req)
	ev := res.Events[0]
	if !ev.Outcome.Success {
		t.Error("HTTP 404 should be success per SDK semantics")
	}
	if ev.EventName != "api.request" {
		t.Errorf("event_name = %q, want api.request", ev.EventName)
	}
}

func TestSpansToEvents_OTLPErrorForcesFailure(t *testing.T) {
	req := minimalRequest("svc", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{intAttr("http.response.status_code", 200)}
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "forced error"}
	res := SpansToEvents(req)
	ev := res.Events[0]
	if ev.Outcome.Success {
		t.Error("OTLP ERROR should force failure even with HTTP 200")
	}
	if ev.Error == nil || ev.Error.Code != "OTEL_ERROR" {
		t.Errorf("expected OTEL_ERROR code, got %v", ev.Error)
	}
}

func TestSpansToEvents_CallerService(t *testing.T) {
	parentSpanID := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	childSpanID := []byte{0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	traceID := hexTraceID()

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
					strAttr("service.name", "gateway"),
					strAttr("deployment.environment", "prod"),
				}},
				ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
					TraceId: traceID, SpanId: parentSpanID, Name: "gateway-op",
					StartTimeUnixNano: 1000000000, EndTimeUnixNano: 1050000000,
					Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				}}}},
			},
			{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
					strAttr("service.name", "checkout"),
					strAttr("deployment.environment", "prod"),
				}},
				ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
					TraceId: traceID, SpanId: childSpanID, ParentSpanId: parentSpanID, Name: "checkout-op",
					StartTimeUnixNano: 1010000000, EndTimeUnixNano: 1040000000,
					Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				}}}},
			},
		},
	}

	res := SpansToEvents(req)
	if len(res.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(res.Events))
	}

	var checkoutEv *event.WideEvent
	for _, ev := range res.Events {
		if ev.System.Service == "checkout" {
			checkoutEv = ev
		}
	}
	if checkoutEv == nil {
		t.Fatal("checkout event not found")
	}
	if checkoutEv.System.CallerService != "gateway" {
		t.Errorf("caller_service = %q, want gateway", checkoutEv.System.CallerService)
	}
}

func TestSpansToEvents_CallerService_SameService_NoSelfCall(t *testing.T) {
	parentSpanID := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	childSpanID := []byte{0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
	traceID := hexTraceID()

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				strAttr("service.name", "svc"),
				strAttr("deployment.environment", "prod"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
				{TraceId: traceID, SpanId: parentSpanID, Name: "op1", StartTimeUnixNano: 1000000000, EndTimeUnixNano: 1050000000, Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}},
				{TraceId: traceID, SpanId: childSpanID, ParentSpanId: parentSpanID, Name: "op2", StartTimeUnixNano: 1010000000, EndTimeUnixNano: 1040000000, Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}},
			}}},
		}},
	}

	res := SpansToEvents(req)
	for _, ev := range res.Events {
		if ev.System.CallerService != "" {
			t.Errorf("expected no caller_service for same-service span, got %q", ev.System.CallerService)
		}
	}
}

func TestSpansToEvents_DownstreamService(t *testing.T) {
	req := minimalRequest("gateway", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Kind = tracepb.Span_SPAN_KIND_CLIENT
	span.Attributes = []*commonpb.KeyValue{strAttr("peer.service", "checkout")}
	res := SpansToEvents(req)
	ev := res.Events[0]
	if ev.System.DownstreamService != "checkout" {
		t.Errorf("downstream_service = %q, want checkout", ev.System.DownstreamService)
	}
}

func TestSpansToEvents_DeploymentEnvironmentName(t *testing.T) {
	req := minimalRequest("svc", hexTraceID(), hexSpanID())
	req.ResourceSpans[0].Resource.Attributes = []*commonpb.KeyValue{
		strAttr("service.name", "svc"),
		strAttr("deployment.environment.name", "staging"),
		strAttr("deployment.environment", "prod"),
	}
	res := SpansToEvents(req)
	ev := res.Events[0]
	if ev.System.Env != "staging" {
		t.Errorf("env = %q, want staging (deployment.environment.name takes precedence)", ev.System.Env)
	}
}

func TestSpansToEvents_RouteTemplateOnlyFromHTTPRoute(t *testing.T) {
	req := minimalRequest("svc", hexTraceID(), hexSpanID())
	res := SpansToEvents(req)
	ev := res.Events[0]
	if ev.Request.RouteTemplate != "" {
		t.Errorf("route_template should be empty without http.route attr, got %q", ev.Request.RouteTemplate)
	}
}
