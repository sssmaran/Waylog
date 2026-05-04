package convert

import (
	"testing"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
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
					strAttr("service.version", "1.2.3"),
					strAttr("deployment.environment", "prod"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           traceID,
					SpanId:            spanID,
					Name:              "GET /api/users",
					StartTimeUnixNano: 1_000_000_000,
					EndTimeUnixNano:   1_050_000_000,
					Attributes: []*commonpb.KeyValue{
						strAttr("http.request.method", "GET"),
						strAttr("http.route", "/api/users"),
						intAttr("http.response.status_code", 200),
					},
					Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
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

func TestSpansToEvents_MinimalHTTPSpan(t *testing.T) {
	req := minimalRequest("checkout", hexTraceID(), hexSpanID())
	res := SpansToEvents(req)

	if res.Dropped != 0 {
		t.Errorf("expected 0 dropped, got %d", res.Dropped)
	}
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	ev := res.Events[0]
	requireValidV2(t, ev)

	if ev.SchemaVersion != eventv2.SchemaVersion2 {
		t.Errorf("schema_version=%q", ev.SchemaVersion)
	}
	if ev.EventID == "" {
		t.Fatal("event_id is empty")
	}
	if ev.Service != "checkout" {
		t.Errorf("service=%q want checkout", ev.Service)
	}
	if ev.Env != "prod" {
		t.Errorf("env=%q want prod", ev.Env)
	}
	if ev.Version != "1.2.3" {
		t.Errorf("version=%q want 1.2.3", ev.Version)
	}
	if ev.TraceID != "aabbccddeeff00112233445566778899" {
		t.Errorf("trace_id=%q", ev.TraceID)
	}
	if ev.SpanID != "aabbccddeeff0011" {
		t.Errorf("span_id=%q", ev.SpanID)
	}
	if ev.Kind != "http" {
		t.Errorf("kind=%q want http", ev.Kind)
	}
	if ev.Status != eventv2.StatusOK {
		t.Errorf("status=%q want ok", ev.Status)
	}
	if ev.DurationMS != 50 {
		t.Errorf("duration_ms=%d want 50", ev.DurationMS)
	}
	if len(ev.Steps) != 1 || ev.Steps[0].Name != "/api/users" || ev.Steps[0].Status != eventv2.StepStatusOK {
		t.Fatalf("steps=%+v", ev.Steps)
	}
	if ev.Anchor != nil {
		t.Fatalf("anchor=%+v want nil", ev.Anchor)
	}
}

func TestSpansToEvents_MissingServiceName(t *testing.T) {
	req := minimalRequest("", hexTraceID(), hexSpanID())
	req.ResourceSpans[0].Resource.Attributes = req.ResourceSpans[0].Resource.Attributes[2:]
	res := SpansToEvents(req)
	if res.Dropped != 1 || len(res.Events) != 0 {
		t.Fatalf("dropped=%d events=%d", res.Dropped, len(res.Events))
	}
	if len(res.Drops) != 1 || res.Drops[0].Reason != DropMissingService {
		t.Errorf("drops=%+v", res.Drops)
	}
}

func TestSpansToEvents_MissingTraceID(t *testing.T) {
	req := minimalRequest("svc", nil, hexSpanID())
	res := SpansToEvents(req)
	if res.Dropped != 1 || len(res.Drops) != 1 || res.Drops[0].Reason != DropMissingTraceID {
		t.Fatalf("res=%+v", res)
	}
}

func TestSpansToEvents_MissingSpanID(t *testing.T) {
	req := minimalRequest("svc", hexTraceID(), nil)
	res := SpansToEvents(req)
	if res.Dropped != 1 || len(res.Events) != 0 || len(res.Drops) != 1 || res.Drops[0].Reason != DropInvalidSpan {
		t.Fatalf("res=%+v", res)
	}
}

func TestSpansToEvents_DropsNonHTTPSpan(t *testing.T) {
	req := minimalRequest("worker", hexTraceID(), hexSpanID())
	req.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes = nil
	res := SpansToEvents(req)
	if res.Dropped != 1 || len(res.Events) != 0 || len(res.Drops) != 1 || res.Drops[0].Reason != DropUnsupportedKind {
		t.Fatalf("res=%+v", res)
	}
}

func TestSpansToEvents_HTTP404IsOK(t *testing.T) {
	req := minimalRequest("api", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{intAttr("http.response.status_code", 404), strAttr("http.request.method", "GET")}
	res := SpansToEvents(req)
	ev := res.Events[0]
	requireValidV2(t, ev)
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("status=%q want ok", ev.Status)
	}
	if ev.Anchor != nil {
		t.Fatalf("anchor=%+v want nil", ev.Anchor)
	}
}

func TestSpansToEvents_HTTP500CreatesAnchorStepAndLog(t *testing.T) {
	req := minimalRequest("payment", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = []*commonpb.KeyValue{
		intAttr("http.response.status_code", 500),
		strAttr("http.request.method", "POST"),
		strAttr("http.route", "/charge"),
	}
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "upstream gateway 5xx"}

	res := SpansToEvents(req)
	if len(res.Events) != 1 {
		t.Fatalf("events=%d", len(res.Events))
	}
	ev := res.Events[0]
	requireValidV2(t, ev)
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%q want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "/charge" || ev.Anchor.ErrorCode != "HTTP_500" {
		t.Fatalf("anchor=%+v", ev.Anchor)
	}
	if len(ev.Steps) != 1 || ev.Steps[0].Status != eventv2.StepStatusError || ev.Steps[0].Error == nil || ev.Steps[0].Error.Code != "HTTP_500" {
		t.Fatalf("steps=%+v", ev.Steps)
	}
	if len(ev.Logs) != 1 || ev.Logs[0].Level != eventv2.LogLevelError || ev.Logs[0].Msg != "upstream gateway 5xx" {
		t.Fatalf("logs=%+v", ev.Logs)
	}
}

func TestSpansToEvents_WaylogAttrsOverrideErrorFamilyAndDownstream(t *testing.T) {
	req := minimalRequest("checkout", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Kind = tracepb.Span_SPAN_KIND_CLIENT
	span.Attributes = []*commonpb.KeyValue{
		intAttr("http.response.status_code", 502),
		strAttr("http.request.method", "POST"),
		strAttr("http.route", "/charge"),
		strAttr("waylog.step", "payment.charge"),
		strAttr("waylog.error_code", "PMT_502"),
		strAttr("peer.service", "payment"),
	}
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "payment unavailable"}

	res := SpansToEvents(req)
	ev := res.Events[0]
	requireValidV2(t, ev)
	if ev.Anchor == nil || ev.Anchor.Step != "payment.charge" || ev.Anchor.ErrorCode != "PMT_502" {
		t.Fatalf("anchor=%+v", ev.Anchor)
	}
	step := ev.Steps[0]
	if step.Name != "payment.charge" || step.Downstream == nil || step.Downstream.Service != "payment" {
		t.Fatalf("step=%+v", step)
	}
	if len(ev.Errors) != 1 || ev.Errors[0].Code != "PMT_502" {
		t.Fatalf("errors=%+v", ev.Errors)
	}
}

func TestSpansToEvents_ExceptionTypeIsFallbackErrorCode(t *testing.T) {
	req := minimalRequest("checkout", hexTraceID(), hexSpanID())
	span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
	span.Events = []*tracepb.Span_Event{{
		Name: "exception",
		Attributes: []*commonpb.KeyValue{
			strAttr("exception.type", "CheckoutError"),
			strAttr("exception.message", "cart validation failed"),
		},
	}}

	res := SpansToEvents(req)
	ev := res.Events[0]
	requireValidV2(t, ev)
	if ev.Anchor == nil || ev.Anchor.ErrorCode != "CheckoutError" {
		t.Fatalf("anchor=%+v", ev.Anchor)
	}
	if len(ev.Logs) != 1 || ev.Logs[0].Msg != "cart validation failed" {
		t.Fatalf("logs=%+v", ev.Logs)
	}
}

func requireValidV2(t *testing.T, ev *eventv2.Event) {
	t.Helper()
	if err := eventv2.Validate("../../../docs/schema/v2.0.json", ev); err != nil {
		t.Fatalf("event must validate against schema: %v\n%+v", err, ev)
	}
}
