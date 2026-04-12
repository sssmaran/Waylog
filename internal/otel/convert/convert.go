// Package convert translates OTLP ExportTraceServiceRequest payloads into
// WideEvents. It is a pure function package — no I/O, no dependencies beyond
// pkg/event and the OTLP proto types — so it can be reused by a future
// OpenTelemetry Collector exporter without dragging in the ingest stack.
package convert

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// DropReason is a bounded enum for why a span could not be converted.
// The set is intentionally small so Prometheus label cardinality stays bounded.
type DropReason string

const (
	DropMissingService  DropReason = "missing_service"
	DropMissingTraceID  DropReason = "missing_trace_id"
	DropInvalidSpan     DropReason = "invalid_span"
	DropFutureTimestamp DropReason = "future_timestamp"
)

// DropEntry records a single dropped span and the reason.
type DropEntry struct {
	SpanName string
	Reason   DropReason
}

// Result is what SpansToEvents returns. Events are not yet validated; the
// caller runs them through the ingest pipeline's validator.
type Result struct {
	Events  []*event.WideEvent
	Dropped int
	Drops   []DropEntry
}

// spanKey is the (trace_id, span_id) key used to resolve caller service
// across the resource_spans boundary in a single request.
type spanKey struct {
	TraceID string
	SpanID  string
}

// SpansToEvents converts an OTLP ExportTraceServiceRequest into WideEvents.
// Best-effort: spans that can't be meaningfully converted are dropped and
// counted. The parent-span index is scoped to this request only.
func SpansToEvents(req *coltracepb.ExportTraceServiceRequest) Result {
	var res Result
	if req == nil {
		return res
	}

	// First pass: build (trace_id, span_id) → service_name index so child
	// spans can discover their parent's service for caller_service.
	idx := make(map[spanKey]string)
	for _, rs := range req.GetResourceSpans() {
		svc := resourceAttr(rs.GetResource(), "service.name")
		if svc == "" {
			continue
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				tid := hex.EncodeToString(span.GetTraceId())
				sid := hex.EncodeToString(span.GetSpanId())
				if tid != "" && sid != "" {
					idx[spanKey{TraceID: tid, SpanID: sid}] = svc
				}
			}
		}
	}

	// Second pass: convert each span.
	for _, rs := range req.GetResourceSpans() {
		rc := extractResourceContext(rs.GetResource())
		if rc.service == "" {
			for _, ss := range rs.GetScopeSpans() {
				for _, span := range ss.GetSpans() {
					res.Dropped++
					res.Drops = append(res.Drops, DropEntry{
						SpanName: span.GetName(),
						Reason:   DropMissingService,
					})
				}
			}
			continue
		}

		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				ev, drop := convertSpan(span, rc, idx)
				if drop != nil {
					res.Dropped++
					res.Drops = append(res.Drops, *drop)
					continue
				}
				res.Events = append(res.Events, ev)
			}
		}
	}

	return res
}

type resourceContext struct {
	service string
	version string
	env     string
}

func extractResourceContext(r *resourcepb.Resource) resourceContext {
	rc := resourceContext{
		service: resourceAttr(r, "service.name"),
		version: resourceAttr(r, "service.version"),
	}
	// Prefer new semconv (deployment.environment.name) over legacy (deployment.environment).
	rc.env = resourceAttr(r, "deployment.environment.name")
	if rc.env == "" {
		rc.env = resourceAttr(r, "deployment.environment")
	}
	if rc.env == "" {
		rc.env = "unknown"
	}
	return rc
}

func convertSpan(span *tracepb.Span, rc resourceContext, idx map[spanKey]string) (*event.WideEvent, *DropEntry) {
	if allZeros(span.GetTraceId()) {
		return nil, &DropEntry{SpanName: span.GetName(), Reason: DropMissingTraceID}
	}
	// Reject empty/zero span IDs: the graph builder skips span records when
	// Request.SpanID is empty, which would leave trace drill-down incomplete
	// for a request the overview now counts as accepted.
	if allZeros(span.GetSpanId()) {
		return nil, &DropEntry{SpanName: span.GetName(), Reason: DropInvalidSpan}
	}
	traceID := hex.EncodeToString(span.GetTraceId())
	spanID := hex.EncodeToString(span.GetSpanId())

	parentSpanID := ""
	if !allZeros(span.GetParentSpanId()) {
		parentSpanID = hex.EncodeToString(span.GetParentSpanId())
	}

	attrs := spanAttrs(span)
	statusCode, success, hasHTTP := deriveOutcome(span, attrs)

	kind := "internal"
	if hasHTTP {
		kind = "http"
	} else if _, ok := attrs["rpc.system"]; ok {
		kind = "rpc"
	}

	eventName := rc.service + ".request"
	if !success {
		eventName = rc.service + ".error"
	}

	var errCtx *event.ErrorContext
	if !success {
		errCtx = extractError(span)
	}

	httpMethod, _ := attrs["http.request.method"].(string)
	httpRoute, _ := attrs["http.route"].(string)

	var downstream string
	if span.GetKind() == tracepb.Span_SPAN_KIND_CLIENT || span.GetKind() == tracepb.Span_SPAN_KIND_PRODUCER {
		downstream = resolveDownstream(attrs)
	}

	var caller string
	if parentSpanID != "" {
		if parentSvc, ok := idx[spanKey{TraceID: traceID, SpanID: parentSpanID}]; ok && parentSvc != rc.service {
			caller = parentSvc
		}
	}

	startNano := span.GetStartTimeUnixNano()
	endNano := span.GetEndTimeUnixNano()
	var latencyMs int64
	if endNano > startNano {
		latencyMs = int64((endNano - startNano) / 1_000_000)
	}

	ev := &event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     eventName,
		Timestamp:     time.Unix(0, int64(startNano)),
		Request: event.RequestContext{
			TraceID:       traceID,
			SpanID:        spanID,
			ParentSpanID:  parentSpanID,
			HTTPMethod:    httpMethod,
			RouteTemplate: httpRoute,
		},
		System: event.SystemContext{
			Service:           rc.service,
			Version:           rc.version,
			Env:               rc.env,
			CallerService:     caller,
			DownstreamService: downstream,
		},
		Outcome: event.OutcomeContext{
			Success:    success,
			StatusCode: statusCode,
			Kind:       kind,
		},
		Error:   errCtx,
		Metrics: event.MetricsContext{LatencyMs: latencyMs},
	}
	return ev, nil
}

// deriveOutcome follows the spec: HTTP status code first, then gRPC, then
// OTLP status. Any OTLP ERROR or exception event forces failure even when
// HTTP indicated success.
func deriveOutcome(span *tracepb.Span, attrs map[string]any) (statusCode int, success bool, hasHTTP bool) {
	if v, ok := attrs["http.response.status_code"]; ok {
		if code, ok := v.(int64); ok && code > 0 {
			statusCode = int(code)
			success = statusCode < 500
			hasHTTP = true
			if success && isOTLPError(span) {
				success = false
			}
			return
		}
	}

	if _, ok := attrs["http.request.method"]; ok {
		hasHTTP = true
	}

	if v, ok := attrs["rpc.grpc.status_code"]; ok {
		if code, ok := v.(int64); ok {
			statusCode = grpcToHTTP(int(code))
			success = code == 0
			return
		}
	}

	status := span.GetStatus()
	if status != nil && status.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		return 500, false, hasHTTP
	}
	return 200, true, hasHTTP
}

func isOTLPError(span *tracepb.Span) bool {
	if s := span.GetStatus(); s != nil && s.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		return true
	}
	for _, e := range span.GetEvents() {
		if e.GetName() == "exception" {
			return true
		}
	}
	return false
}

func extractError(span *tracepb.Span) *event.ErrorContext {
	for _, e := range span.GetEvents() {
		if e.GetName() != "exception" {
			continue
		}
		code := eventAttr(e, "exception.type")
		msg := eventAttr(e, "exception.message")
		if code != "" {
			return &event.ErrorContext{Code: code, Message: msg}
		}
	}
	if s := span.GetStatus(); s != nil && s.GetMessage() != "" {
		return &event.ErrorContext{Code: "OTEL_ERROR", Message: s.GetMessage()}
	}
	return &event.ErrorContext{Code: "OTEL_ERROR"}
}

func resolveDownstream(attrs map[string]any) string {
	if v, ok := attrs["peer.service"].(string); ok && v != "" {
		return v
	}
	if v, ok := attrs["server.address"].(string); ok && v != "" {
		host := strings.TrimPrefix(v, "http://")
		host = strings.TrimPrefix(host, "https://")
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		return host
	}
	return ""
}

// grpcToHTTP maps gRPC status codes to approximate HTTP equivalents so the
// outcome status code stays consistent across transports.
func grpcToHTTP(code int) int {
	switch code {
	case 0:
		return 200
	case 1:
		return 499
	case 2:
		return 500
	case 3:
		return 400
	case 4:
		return 504
	case 5:
		return 404
	case 6:
		return 409
	case 7:
		return 403
	case 8:
		return 429
	case 9:
		return 400
	case 10:
		return 409
	case 11:
		return 400
	case 12:
		return 501
	case 13:
		return 500
	case 14:
		return 503
	case 15:
		return 500
	case 16:
		return 401
	default:
		return 500
	}
}

func resourceAttr(r *resourcepb.Resource, key string) string {
	if r == nil {
		return ""
	}
	for _, kv := range r.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func spanAttrs(span *tracepb.Span) map[string]any {
	m := make(map[string]any, len(span.GetAttributes()))
	for _, kv := range span.GetAttributes() {
		m[kv.GetKey()] = anyValue(kv.GetValue())
	}
	return m
}

func eventAttr(e *tracepb.Span_Event, key string) string {
	for _, kv := range e.GetAttributes() {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func anyValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch val := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return val.IntValue
	case *commonpb.AnyValue_BoolValue:
		return val.BoolValue
	case *commonpb.AnyValue_DoubleValue:
		return val.DoubleValue
	default:
		return nil
	}
}

func allZeros(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
