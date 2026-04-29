package ingestv2

import (
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestOrderTraceCausalDepthFirst(t *testing.T) {
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(0))
	root.Steps = []eventv2.Step{{Name: "call.checkout", SpanID: "span-checkout", Status: "ok"}}
	child := testTraceEvent("child", "trace", "checkout", eventv2.StatusOK, testTime(1))
	child.ParentSpanID = "span-checkout"
	child.Steps = []eventv2.Step{{Name: "call.payment", SpanID: "span-payment", Status: "ok"}}
	leaf := testTraceEvent("leaf", "trace", "payment", eventv2.StatusOK, testTime(2))
	leaf.ParentSpanID = "span-payment"

	ordered, linkage := OrderTrace([]*eventv2.Event{leaf, child, root})
	if linkage != LinkageCausal {
		t.Fatalf("linkage=%s", linkage)
	}
	if ids(ordered) != "root,child,leaf" {
		t.Fatalf("order=%s", ids(ordered))
	}
}

func TestOrderTracePartialLinkageFallsBack(t *testing.T) {
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(2))
	root.Steps = []eventv2.Step{{Name: "payment.charge", Status: "ok"}}
	child := testTraceEvent("child", "trace", "payment", eventv2.StatusOK, testTime(1))
	child.ParentSpanID = "missing"

	ordered, linkage := OrderTrace([]*eventv2.Event{root, child})
	if linkage != LinkageTimestampFallback {
		t.Fatalf("linkage=%s", linkage)
	}
	if ids(ordered) != "child,root" {
		t.Fatalf("order=%s", ids(ordered))
	}
}

func TestResolveAnchorDeepestFailedLeaf(t *testing.T) {
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusError, testTime(0))
	root.Anchor = &eventv2.Anchor{Step: "call.checkout", ErrorCode: "GATEWAY"}
	root.Steps = []eventv2.Step{{Name: "call.checkout", SpanID: "span-checkout", Status: "error"}}
	child := testTraceEvent("child", "trace", "checkout", eventv2.StatusError, testTime(1))
	child.ParentSpanID = "span-checkout"
	child.Anchor = &eventv2.Anchor{Step: "call.payment", ErrorCode: "CHECKOUT"}
	child.Steps = []eventv2.Step{{Name: "call.payment", SpanID: "span-payment", Status: "error"}}
	leaf := testTraceEvent("leaf", "trace", "payment", eventv2.StatusError, testTime(2))
	leaf.ParentSpanID = "span-payment"
	leaf.Anchor = &eventv2.Anchor{Step: "charge", ErrorCode: "PAYMENT"}

	result := ResolveAnchor([]*eventv2.Event{root, leaf, child})
	if result.Linkage != LinkageCausal || result.Event.EventID != "leaf" {
		t.Fatalf("result=%+v", result)
	}
}

func TestResolveAnchorFallbackDoesNotParseStepName(t *testing.T) {
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(0))
	root.Steps = []eventv2.Step{{Name: "payment.charge", Status: "ok"}}
	payment := testTraceEvent("payment", "trace", "payment", eventv2.StatusError, testTime(1))
	payment.Anchor = &eventv2.Anchor{Step: "charge", ErrorCode: "PAYMENT"}

	result := ResolveAnchor([]*eventv2.Event{payment, root})
	if result.Linkage != LinkageTimestampFallback || result.Event.EventID != "payment" {
		t.Fatalf("result=%+v", result)
	}
}

func TestResolveAnchorNoFailuresRootAndSuppressedOnly(t *testing.T) {
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(0))
	child := testTraceEvent("child", "trace", "checkout", eventv2.StatusOK, testTime(1))
	child.ParentSpanID = "missing"

	result := ResolveAnchor([]*eventv2.Event{child, root})
	if result.Event.EventID != "root" {
		t.Fatalf("root=%s", result.Event.EventID)
	}

	suppressed := testTraceEvent("suppressed", "trace2", "gateway", eventv2.StatusSuppressed, testTime(0))
	result = ResolveAnchor([]*eventv2.Event{suppressed})
	if result.Event.EventID != "suppressed" || result.Event.Status != eventv2.StatusSuppressed {
		t.Fatalf("suppressed result=%+v", result)
	}
}

func TestResolveAnchorOptionsExcludeSuppressed(t *testing.T) {
	suppressedRoot := testTraceEvent("suppressed-root", "trace", "gateway", eventv2.StatusSuppressed, testTime(0))
	okChild := testTraceEvent("ok-child", "trace", "checkout", eventv2.StatusOK, testTime(1))
	okChild.ParentSpanID = "missing"

	result := ResolveAnchor([]*eventv2.Event{suppressedRoot, okChild})
	if result.Event.EventID != "suppressed-root" {
		t.Fatalf("default root=%s", result.Event.EventID)
	}
	result = ResolveAnchorWithOptions([]*eventv2.Event{suppressedRoot, okChild}, ResolveOpts{ExcludeSuppressed: true})
	if result.Event.EventID != "ok-child" {
		t.Fatalf("excluded root=%s", result.Event.EventID)
	}

	result = ResolveAnchorWithOptions([]*eventv2.Event{suppressedRoot}, ResolveOpts{ExcludeSuppressed: true})
	if result.Event != nil {
		t.Fatalf("result=%+v want nil event", result)
	}
}

func TestResolveAnchorUnsatisfiedParentRootBranch(t *testing.T) {
	orphan := testTraceEvent("orphan", "trace", "gateway", eventv2.StatusOK, testTime(1))
	orphan.ParentSpanID = "missing"
	late := testTraceEvent("late", "trace", "checkout", eventv2.StatusOK, testTime(2))
	late.ParentSpanID = "also-missing"

	result := ResolveAnchor([]*eventv2.Event{late, orphan})
	if result.Event.EventID != "orphan" || result.Linkage != LinkageTimestampFallback {
		t.Fatalf("result=%+v", result)
	}
}

func testTime(seconds int) time.Time {
	return time.Date(2026, 4, 25, 14, 0, seconds, 0, time.UTC)
}

func testTraceEvent(id, traceID, service string, status eventv2.Status, ts time.Time) *eventv2.Event {
	return &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       id,
		TsStart:       ts,
		TsEnd:         ts.Add(10 * time.Millisecond),
		DurationMS:    10,
		Kind:          "http",
		Service:       service,
		Env:           "test",
		TraceID:       traceID,
		SpanID:        id + "-span",
		Status:        status,
	}
}

func ids(events []*eventv2.Event) string {
	out := ""
	for i, ev := range events {
		if i > 0 {
			out += ","
		}
		out += ev.EventID
	}
	return out
}
