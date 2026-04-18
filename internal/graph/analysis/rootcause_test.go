package analysis

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

const (
	testTraceID    = "0123456789abcdef0123456789abcdef"
	spanRoot       = "aaaaaaaaaaaaaaaa"
	spanChild      = "bbbbbbbbbbbbbbbb"
	spanGrandchild = "cccccccccccccccc"
	spanSibling    = "dddddddddddddddd"
)

func testRequestNode() core.Node {
	return testutil.MakeNode(
		core.ID("request", testTraceID),
		core.NodeRequest,
		map[string]any{"trace_id": testTraceID},
	)
}

func TestRootCauseSpan_TraceStore_DeepestFailingSpanWins(t *testing.T) {
	reqID := core.ID("request", testTraceID)
	g := testutil.MakeGraph([]core.Node{testRequestNode()}, nil)

	ts := tracestore.NewStore()
	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:    spanRoot,
		Service:   "gateway",
		ErrorCode: "GW_DOWNSTREAM",
		Success:   false,
		Timestamp: base,
	})
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanChild,
		ParentSpanID: spanRoot,
		Service:      "checkout",
		ErrorCode:    "CHK_DOWNSTREAM",
		Success:      false,
		Timestamp:    base.Add(time.Millisecond),
	})
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanGrandchild,
		ParentSpanID: spanChild,
		Service:      "payment",
		ErrorCode:    "PMT_502",
		Success:      false,
		Timestamp:    base.Add(2 * time.Millisecond),
	})

	id, code, ok := RootCauseSpan(g, ts, reqID)
	if !ok {
		t.Fatal("RootCauseSpan returned ok=false; want ok=true")
	}
	if id != spanGrandchild {
		t.Errorf("spanID = %s, want %s (deepest failing span)", id, spanGrandchild)
	}
	if code != "PMT_502" {
		t.Errorf("errorCode = %s, want PMT_502", code)
	}
}

func TestRootCauseSpan_TraceStore_EarliestTimestampBreaksDepthTie(t *testing.T) {
	reqID := core.ID("request", testTraceID)
	g := testutil.MakeGraph([]core.Node{testRequestNode()}, nil)

	ts := tracestore.NewStore()
	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:    spanRoot,
		Service:   "gateway",
		Success:   true,
		Timestamp: base,
	})
	// Two children at equal depth under root. The earlier one is the root cause.
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanChild,
		ParentSpanID: spanRoot,
		Service:      "checkout",
		ErrorCode:    "ERR_LATE",
		Success:      false,
		Timestamp:    base.Add(20 * time.Millisecond),
	})
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanSibling,
		ParentSpanID: spanRoot,
		Service:      "payment",
		ErrorCode:    "ERR_EARLY",
		Success:      false,
		Timestamp:    base.Add(5 * time.Millisecond),
	})

	id, code, ok := RootCauseSpan(g, ts, reqID)
	if !ok {
		t.Fatal("RootCauseSpan returned ok=false; want ok=true")
	}
	if id != spanSibling {
		t.Errorf("spanID = %s, want %s (earlier timestamp wins)", id, spanSibling)
	}
	if code != "ERR_EARLY" {
		t.Errorf("errorCode = %s, want ERR_EARLY", code)
	}
}

func TestRootCauseSpan_TraceStore_LexIDBreaksRemainingTies(t *testing.T) {
	reqID := core.ID("request", testTraceID)
	g := testutil.MakeGraph([]core.Node{testRequestNode()}, nil)

	ts := tracestore.NewStore()
	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:    spanRoot,
		Service:   "gateway",
		Success:   true,
		Timestamp: base,
	})
	// Two children at equal depth and timestamp — lex lowest id wins.
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanSibling, // "dddd..."
		ParentSpanID: spanRoot,
		Service:      "payment",
		ErrorCode:    "ERR_D",
		Success:      false,
		Timestamp:    base.Add(5 * time.Millisecond),
	})
	ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
		SpanID:       spanChild, // "bbbb..."
		ParentSpanID: spanRoot,
		Service:      "checkout",
		ErrorCode:    "ERR_B",
		Success:      false,
		Timestamp:    base.Add(5 * time.Millisecond),
	})

	id, _, ok := RootCauseSpan(g, ts, reqID)
	if !ok {
		t.Fatal("RootCauseSpan returned ok=false; want ok=true")
	}
	if id != spanChild {
		t.Errorf("spanID = %s, want %s (lex-lowest id wins)", id, spanChild)
	}
}

func TestRootCauseSpan_GraphFallback(t *testing.T) {
	// No trace store — exercises the graph-span-node fallback path.
	reqID := core.ID("request", testTraceID)
	rootSpan := core.ID("span", testTraceID, spanRoot)
	childSpan := core.ID("span", testTraceID, spanChild)
	errShallow := core.ID("error", "SHALLOW_ERR")
	errDeep := core.ID("error", "DEEP_ERR")

	g := testutil.MakeGraph(
		[]core.Node{
			testRequestNode(),
			testutil.MakeNode(rootSpan, core.NodeSpan, map[string]any{"trace_id": testTraceID}),
			testutil.MakeNode(childSpan, core.NodeSpan, map[string]any{"trace_id": testTraceID, "parent_span_id": spanRoot}),
			testutil.MakeNode(errShallow, core.NodeError, map[string]any{"code": "SHALLOW_ERR"}),
			testutil.MakeNode(errDeep, core.NodeError, map[string]any{"code": "DEEP_ERR"}),
		},
		[]core.Edge{
			{From: reqID, To: rootSpan, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: childSpan, Type: core.EdgeRequestHasSpan},
			{From: childSpan, To: rootSpan, Type: core.EdgeSpanChildOf},
			{From: rootSpan, To: errShallow, Type: core.EdgeFailedWith},
			{From: childSpan, To: errDeep, Type: core.EdgeFailedWith},
		},
	)

	id, code, ok := RootCauseSpan(g, nil, reqID)
	if !ok {
		t.Fatal("RootCauseSpan returned ok=false; want ok=true")
	}
	if id != childSpan {
		t.Errorf("spanID = %s, want %s (deepest graph span)", id, childSpan)
	}
	if code != "DEEP_ERR" {
		t.Errorf("errorCode = %s, want DEEP_ERR", code)
	}
}

func TestRootCauseSpan_RequestLevelErrorFallback(t *testing.T) {
	// No span data at all — only an EdgeFailedWith from the request.
	reqID := core.ID("request", testTraceID)
	errID := core.ID("error", "UNATTRIBUTED")

	g := testutil.MakeGraph(
		[]core.Node{
			testRequestNode(),
			testutil.MakeNode(errID, core.NodeError, map[string]any{"code": "UNATTRIBUTED"}),
		},
		[]core.Edge{
			{From: reqID, To: errID, Type: core.EdgeFailedWith},
		},
	)

	id, code, ok := RootCauseSpan(g, nil, reqID)
	if !ok {
		t.Fatal("RootCauseSpan returned ok=false; want ok=true")
	}
	if id != "" {
		t.Errorf("spanID = %s, want empty (request-level error)", id)
	}
	if code != "UNATTRIBUTED" {
		t.Errorf("errorCode = %s, want UNATTRIBUTED", code)
	}
}

func TestRootCauseSpan_NoFailures(t *testing.T) {
	// Request exists but has no error information — returns ok=false.
	reqID := core.ID("request", testTraceID)
	g := testutil.MakeGraph([]core.Node{testRequestNode()}, nil)

	_, _, ok := RootCauseSpan(g, nil, reqID)
	if ok {
		t.Error("RootCauseSpan returned ok=true; want ok=false for request with no errors")
	}
}

func TestRootCauseSpan_StableUnderArrivalOrder(t *testing.T) {
	// Insert the same three spans in two different orders — same root cause.
	reqID := core.ID("request", testTraceID)
	g := testutil.MakeGraph([]core.Node{testRequestNode()}, nil)
	base := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	build := func() *tracestore.Store {
		ts := tracestore.NewStore()
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanRoot, Service: "gateway", ErrorCode: "GW", Success: false, Timestamp: base,
		})
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanChild, ParentSpanID: spanRoot, Service: "checkout", ErrorCode: "CHK", Success: false, Timestamp: base.Add(time.Millisecond),
		})
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanGrandchild, ParentSpanID: spanChild, Service: "payment", ErrorCode: "PMT", Success: false, Timestamp: base.Add(2 * time.Millisecond),
		})
		return ts
	}
	buildReverse := func() *tracestore.Store {
		ts := tracestore.NewStore()
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanGrandchild, ParentSpanID: spanChild, Service: "payment", ErrorCode: "PMT", Success: false, Timestamp: base.Add(2 * time.Millisecond),
		})
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanChild, ParentSpanID: spanRoot, Service: "checkout", ErrorCode: "CHK", Success: false, Timestamp: base.Add(time.Millisecond),
		})
		ts.Upsert(testTraceID, reqID, &tracestore.SpanRecord{
			SpanID: spanRoot, Service: "gateway", ErrorCode: "GW", Success: false, Timestamp: base,
		})
		return ts
	}

	idA, codeA, okA := RootCauseSpan(g, build(), reqID)
	idB, codeB, okB := RootCauseSpan(g, buildReverse(), reqID)
	if !okA || !okB {
		t.Fatalf("RootCauseSpan returned ok=false (A=%v, B=%v)", okA, okB)
	}
	if idA != idB || codeA != codeB {
		t.Errorf("root cause unstable under arrival order: (A=%s/%s) vs (B=%s/%s)", idA, codeA, idB, codeB)
	}
	if idA != spanGrandchild {
		t.Errorf("spanID = %s, want %s (deepest)", idA, spanGrandchild)
	}
}

func TestRootCauseSpan_RequestNotFound(t *testing.T) {
	g := core.New()
	_, _, ok := RootCauseSpan(g, nil, "request:missing")
	if ok {
		t.Error("RootCauseSpan returned ok=true for missing request; want false")
	}
}
