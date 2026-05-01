package ingestv2

import (
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestSearchEventsFiltersAndSuppressedDefault(t *testing.T) {
	idx := NewRecentIndex(nil)
	idx.Insert(testTraceEvent("ok", "trace-a", "checkout", eventv2.StatusOK, testTime(0)))
	errEvent := testTraceEvent("err", "trace-b", "payment", eventv2.StatusError, testTime(1))
	errEvent.Anchor = &eventv2.Anchor{Step: "charge", ErrorCode: "PMT_502"}
	idx.Insert(errEvent)
	idx.Insert(testTraceEvent("suppressed", "trace-c", "checkout", eventv2.StatusSuppressed, testTime(2)))

	reader := NewReader(idx)
	result := reader.SearchEvents(SearchFilter{Since: testTime(-1), Until: testTime(10)}, nil, 10)
	if got := ids(result.Events); got != "err,ok" {
		t.Fatalf("default search=%s", got)
	}

	result = reader.SearchEvents(SearchFilter{Service: "payment", ErrorCode: "PMT_502", Since: testTime(-1), Until: testTime(10)}, nil, 10)
	if got := ids(result.Events); got != "err" {
		t.Fatalf("filtered search=%s", got)
	}

	result = reader.SearchEvents(SearchFilter{IncludeSuppressed: true, Since: testTime(-1), Until: testTime(10)}, nil, 10)
	if got := ids(result.Events); got != "suppressed,err,ok" {
		t.Fatalf("include suppressed=%s", got)
	}
}

func TestSearchEventsPaginatesWithCursor(t *testing.T) {
	idx := NewRecentIndex(nil)
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		idx.Insert(testTraceEvent(id, "trace", "svc", eventv2.StatusOK, testTime(i)))
	}
	reader := NewReader(idx)
	filter := SearchFilter{Since: testTime(-1), Until: testTime(10)}

	page1 := reader.SearchEvents(filter, nil, 2)
	if got := ids(page1.Events); got != "e,d" {
		t.Fatalf("page1=%s", got)
	}
	page2 := reader.SearchEvents(filter, page1.NextCursor, 2)
	if got := ids(page2.Events); got != "c,b" {
		t.Fatalf("page2=%s", got)
	}
	page3 := reader.SearchEvents(filter, page2.NextCursor, 2)
	if got := ids(page3.Events); got != "a" {
		t.Fatalf("page3=%s", got)
	}
	if page3.NextCursor != nil {
		t.Fatalf("next=%+v want nil", page3.NextCursor)
	}
}

func TestRecentTracesUsesAnyEventFilterAndFullTraceSummary(t *testing.T) {
	idx := NewRecentIndex(nil)
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(0))
	root.Steps = []eventv2.Step{{Name: "call.payment", SpanID: "span-payment", Status: eventv2.StepStatusOK}}
	payment := testTraceEvent("payment", "trace", "payment", eventv2.StatusError, testTime(1))
	payment.ParentSpanID = "span-payment"
	payment.Anchor = &eventv2.Anchor{Step: "charge", ErrorCode: "PMT:502"}
	idx.Insert(root)
	idx.Insert(payment)

	reader := NewReader(idx)
	result := reader.RecentTraces(SearchFilter{Service: "payment", Since: testTime(-1), Until: testTime(10)}, nil, 10)
	if len(result.Traces) != 1 {
		t.Fatalf("traces=%+v", result.Traces)
	}
	trace := result.Traces[0]
	if trace.TraceID != "trace" || trace.Status != eventv2.StatusError {
		t.Fatalf("trace=%+v", trace)
	}
	if trace.DurationMS != int64(time.Second/time.Millisecond)+10 {
		t.Fatalf("duration=%d", trace.DurationMS)
	}
	if got := stringsJoin(trace.Services); got != "gateway,payment" {
		t.Fatalf("services=%s", got)
	}
	if trace.AnchorSummary == nil || *trace.AnchorSummary != `payment:charge:PMT\:502` {
		t.Fatalf("anchor=%v", trace.AnchorSummary)
	}
}

func TestRecentTracesSuppressedOnlyRequiresOptIn(t *testing.T) {
	idx := NewRecentIndex(nil)
	idx.Insert(testTraceEvent("suppressed", "trace", "gateway", eventv2.StatusSuppressed, testTime(0)))
	reader := NewReader(idx)
	filter := SearchFilter{Since: testTime(-1), Until: testTime(10)}
	if got := reader.RecentTraces(filter, nil, 10); len(got.Traces) != 0 {
		t.Fatalf("traces=%+v want empty", got.Traces)
	}
	filter.IncludeSuppressed = true
	got := reader.RecentTraces(filter, nil, 10)
	if len(got.Traces) != 1 || got.Traces[0].Status != eventv2.StatusSuppressed {
		t.Fatalf("traces=%+v want suppressed-only trace when explicitly included", got.Traces)
	}
}

func TestRecentTracesIncludeSuppressedAddsToRegularResults(t *testing.T) {
	idx := NewRecentIndex(nil)
	idx.Insert(testTraceEvent("ok", "trace-ok", "gateway", eventv2.StatusOK, testTime(0)))
	idx.Insert(testTraceEvent("suppressed", "trace-suppressed", "gateway", eventv2.StatusSuppressed, testTime(1)))
	reader := NewReader(idx)
	filter := SearchFilter{Since: testTime(-1), Until: testTime(10)}
	if got := reader.RecentTraces(filter, nil, 10); len(got.Traces) != 1 || got.Traces[0].TraceID != "trace-ok" {
		t.Fatalf("default traces=%+v want only non-suppressed trace", got.Traces)
	}
	filter.IncludeSuppressed = true
	got := reader.RecentTraces(filter, nil, 10)
	if len(got.Traces) != 2 {
		t.Fatalf("include suppressed traces=%+v want suppressed and non-suppressed", got.Traces)
	}
	if got.Traces[0].TraceID != "trace-suppressed" || got.Traces[1].TraceID != "trace-ok" {
		t.Fatalf("include suppressed order=%+v", got.Traces)
	}
}

func stringsJoin(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ","
		}
		out += part
	}
	return out
}
