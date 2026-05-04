// Package convert translates OTLP ExportTraceServiceRequest payloads into
// schema-2.0 WideEvents. It is a pure function package — no I/O, no ingest
// dependency — so it can be reused by future OTLP entrypoints.
package convert

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
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
	DropUnsupportedKind DropReason = "unsupported_kind"
	DropFutureTimestamp DropReason = "future_timestamp"
)

// DropEntry records a single dropped span and the reason.
type DropEntry struct {
	SpanName string
	Reason   DropReason
}

// Result is what SpansToEvents returns. Events are not yet written; callers run
// them through the schema-2.0 ingest handler.
type Result struct {
	Events  []*eventv2.Event
	Dropped int
	Drops   []DropEntry
}

// SpansToEvents converts OTLP spans into schema-2.0 WideEvents. HTTP spans are
// supported in this slice; non-HTTP spans are dropped until their v2 semantics
// are explicitly defined.
func SpansToEvents(req *coltracepb.ExportTraceServiceRequest) Result {
	var res Result
	if req == nil {
		return res
	}
	for _, rs := range req.GetResourceSpans() {
		rc := extractResourceContext(rs.GetResource())
		if rc.service == "" {
			dropResourceSpans(&res, rs, DropMissingService)
			continue
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				ev, drop := convertSpan(span, rc)
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

func dropResourceSpans(res *Result, rs *tracepb.ResourceSpans, reason DropReason) {
	for _, ss := range rs.GetScopeSpans() {
		for _, span := range ss.GetSpans() {
			res.Dropped++
			res.Drops = append(res.Drops, DropEntry{SpanName: span.GetName(), Reason: reason})
		}
	}
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
	rc.env = resourceAttr(r, "deployment.environment.name")
	if rc.env == "" {
		rc.env = resourceAttr(r, "deployment.environment")
	}
	if rc.env == "" {
		rc.env = "unknown"
	}
	return rc
}

func convertSpan(span *tracepb.Span, rc resourceContext) (*eventv2.Event, *DropEntry) {
	if allZeros(span.GetTraceId()) {
		return nil, &DropEntry{SpanName: span.GetName(), Reason: DropMissingTraceID}
	}
	if allZeros(span.GetSpanId()) {
		return nil, &DropEntry{SpanName: span.GetName(), Reason: DropInvalidSpan}
	}
	attrs := spanAttrs(span)
	statusCode, success, hasHTTP := deriveHTTPOutcome(span, attrs)
	if !hasHTTP {
		return nil, &DropEntry{SpanName: span.GetName(), Reason: DropUnsupportedKind}
	}

	traceID := hex.EncodeToString(span.GetTraceId())
	spanID := hex.EncodeToString(span.GetSpanId())
	parentSpanID := ""
	if !allZeros(span.GetParentSpanId()) {
		parentSpanID = hex.EncodeToString(span.GetParentSpanId())
	}

	start := time.Unix(0, int64(span.GetStartTimeUnixNano())).UTC()
	end := time.Unix(0, int64(span.GetEndTimeUnixNano())).UTC()
	if end.Before(start) {
		end = start
	}
	durationMS := int64(end.Sub(start) / time.Millisecond)

	stepName := stepName(span, attrs)
	errorCode, errorReason := errorInfo(span, attrs, statusCode)
	step := eventv2.Step{
		Name:       stepName,
		SpanID:     spanID,
		StartMS:    0,
		DurationMS: durationMS,
		Status:     eventv2.StepStatusOK,
	}
	if downstream := downstreamFromSpan(span, attrs); downstream != nil {
		step.Downstream = downstream
	}

	status := eventv2.StatusOK
	var anchor *eventv2.Anchor
	var errors []eventv2.ErrorRef
	var logs []eventv2.Log
	if !success {
		status = eventv2.StatusError
		step.Status = eventv2.StepStatusError
		step.Error = &eventv2.StepError{Code: errorCode, Reason: errorReason, Cause: "otlp"}
		anchor = &eventv2.Anchor{Step: stepName, ErrorCode: errorCode, Kind: "otlp"}
		errors = []eventv2.ErrorRef{{Code: errorCode, Reason: errorReason}}
		logs = errorLogs(span, errorCode, errorReason)
	}

	return &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       deterministicEventID(traceID, spanID),
		TsStart:       start,
		TsEnd:         end,
		DurationMS:    durationMS,
		Kind:          "http",
		Service:       rc.service,
		Env:           rc.env,
		Version:       rc.version,
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentSpanID,
		Status:        status,
		Anchor:        anchor,
		Steps:         []eventv2.Step{step},
		Logs:          logs,
		Fields:        fields(span, attrs, statusCode),
		Errors:        errors,
	}, nil
}

func deterministicEventID(traceID, spanID string) string {
	sum := sha256.Sum256([]byte(traceID + ":" + spanID))
	b := append([]byte(nil), sum[:16]...)
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func deriveHTTPOutcome(span *tracepb.Span, attrs map[string]any) (statusCode int, success bool, hasHTTP bool) {
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
	if _, ok := attrs["http.route"]; ok {
		hasHTTP = true
	}
	if isOTLPError(span) {
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

func stepName(span *tracepb.Span, attrs map[string]any) string {
	if v, ok := attrs["waylog.step"].(string); ok && v != "" {
		return v
	}
	if v, ok := attrs["http.route"].(string); ok && v != "" {
		return v
	}
	if span.GetName() != "" {
		return span.GetName()
	}
	return "otlp.span"
}

func errorInfo(span *tracepb.Span, attrs map[string]any, statusCode int) (code, reason string) {
	if v, ok := attrs["waylog.error_code"].(string); ok && v != "" {
		return v, errorReason(span)
	}
	for _, e := range span.GetEvents() {
		if e.GetName() != "exception" {
			continue
		}
		if code := eventAttr(e, "exception.type"); code != "" {
			return code, eventAttr(e, "exception.message")
		}
	}
	if statusCode > 0 {
		return fmt.Sprintf("HTTP_%d", statusCode), errorReason(span)
	}
	return "OTLP_ERROR", errorReason(span)
}

func errorReason(span *tracepb.Span) string {
	for _, e := range span.GetEvents() {
		if e.GetName() == "exception" {
			if msg := eventAttr(e, "exception.message"); msg != "" {
				return msg
			}
		}
	}
	if s := span.GetStatus(); s != nil && s.GetMessage() != "" {
		return s.GetMessage()
	}
	return "otlp span reported error"
}

func errorLogs(span *tracepb.Span, code, reason string) []eventv2.Log {
	msg := reason
	if msg == "" {
		msg = code
	}
	return []eventv2.Log{{
		TsOffsetMS: 0,
		Level:      eventv2.LogLevelError,
		Msg:        msg,
		Fields:     map[string]any{"error_code": code},
	}}
}

func downstreamFromSpan(span *tracepb.Span, attrs map[string]any) *eventv2.Downstream {
	if span.GetKind() != tracepb.Span_SPAN_KIND_CLIENT && span.GetKind() != tracepb.Span_SPAN_KIND_PRODUCER {
		return nil
	}
	service := resolveDownstream(attrs)
	if service == "" {
		return nil
	}
	return &eventv2.Downstream{
		Service:  service,
		Endpoint: downstreamEndpoint(attrs),
		Kind:     "http",
	}
}

func resolveDownstream(attrs map[string]any) string {
	for _, key := range []string{"peer.service", "server.address", "net.peer.name"} {
		if v, ok := attrs[key].(string); ok && v != "" {
			return stripHostPort(v)
		}
	}
	return ""
}

func downstreamEndpoint(attrs map[string]any) string {
	for _, key := range []string{"http.route", "url.path", "http.target"} {
		if v, ok := attrs[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func stripHostPort(v string) string {
	host := strings.TrimPrefix(v, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func fields(span *tracepb.Span, attrs map[string]any, statusCode int) map[string]any {
	httpFields := map[string]any{"status": statusCode}
	if v, ok := attrs["http.request.method"].(string); ok && v != "" {
		httpFields["method"] = v
	}
	if v, ok := attrs["http.route"].(string); ok && v != "" {
		httpFields["route"] = v
	}
	return map[string]any{
		"http": httpFields,
		"otel": map[string]any{
			"span_name": span.GetName(),
			"span_kind": span.GetKind().String(),
		},
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
