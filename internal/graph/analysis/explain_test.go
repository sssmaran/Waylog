package analysis

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestExplainRequest_DeepestSpanRootCause(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	reqID := core.ID("request", traceID)
	rootSpanID := core.ID("span", traceID, "aaaaaaaaaaaaaaaa")
	childSpanID := core.ID("span", traceID, "bbbbbbbbbbbbbbbb")
	grandchildSpanID := core.ID("span", traceID, "cccccccccccccccc")

	errShallow := core.ID("error", "SHALLOW_ERR")
	errDeep := core.ID("error", "DEEP_ERR")
	svcID := core.ID("service", "payment")

	g := testutil.MakeGraph(
		[]core.Node{
			testutil.MakeNode(reqID, core.NodeRequest, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(rootSpanID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(childSpanID, core.NodeSpan, map[string]any{"trace_id": traceID, "parent_span_id": "aaaaaaaaaaaaaaaa"}),
			testutil.MakeNode(grandchildSpanID, core.NodeSpan, map[string]any{"trace_id": traceID, "parent_span_id": "bbbbbbbbbbbbbbbb"}),
			testutil.MakeNode(errShallow, core.NodeError, map[string]any{"code": "SHALLOW_ERR", "message": "shallow"}),
			testutil.MakeNode(errDeep, core.NodeError, map[string]any{"code": "DEEP_ERR", "message": "deep"}),
			testutil.MakeNode(svcID, core.NodeService, map[string]any{"name": "payment"}),
		},
		[]core.Edge{
			{From: reqID, To: rootSpanID, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: childSpanID, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: grandchildSpanID, Type: core.EdgeRequestHasSpan},
			// Parent chain via EdgeSpanChildOf
			{From: childSpanID, To: rootSpanID, Type: core.EdgeSpanChildOf},
			{From: grandchildSpanID, To: childSpanID, Type: core.EdgeSpanChildOf},
			// Errors on root and grandchild spans
			{From: rootSpanID, To: errShallow, Type: core.EdgeFailedWith},
			{From: grandchildSpanID, To: errDeep, Type: core.EdgeFailedWith},
			// Service on grandchild
			{From: grandchildSpanID, To: svcID, Type: core.EdgeSpanOnService},
		},
	)

	ex, err := ExplainRequest(g, reqID)
	if err != nil {
		t.Fatal(err)
	}

	if ex.ErrorCode != "DEEP_ERR" {
		t.Errorf("ErrorCode = %v, want DEEP_ERR", ex.ErrorCode)
	}
	if ex.SpanID != grandchildSpanID {
		t.Errorf("SpanID = %s, want %s", ex.SpanID, grandchildSpanID)
	}
	if ex.SpanDepth != "child" {
		t.Errorf("SpanDepth = %s, want child", ex.SpanDepth)
	}
	if ex.SpanService != "payment" {
		t.Errorf("SpanService = %v, want payment", ex.SpanService)
	}

	// SpanChain should go grandchild → child → root
	if len(ex.SpanChain) != 3 {
		t.Fatalf("SpanChain len = %d, want 3", len(ex.SpanChain))
	}
	if ex.SpanChain[0].SpanID != grandchildSpanID {
		t.Errorf("SpanChain[0].SpanID = %s, want grandchild", ex.SpanChain[0].SpanID)
	}
	if ex.SpanChain[0].Depth != 2 {
		t.Errorf("SpanChain[0].Depth = %d, want 2", ex.SpanChain[0].Depth)
	}
	if ex.SpanChain[1].SpanID != childSpanID {
		t.Errorf("SpanChain[1].SpanID = %s, want child", ex.SpanChain[1].SpanID)
	}
	if ex.SpanChain[2].SpanID != rootSpanID {
		t.Errorf("SpanChain[2].SpanID = %s, want root", ex.SpanChain[2].SpanID)
	}
}

func TestExplainRequest_SameDepthPreferEarlier(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	reqID := core.ID("request", traceID)
	span1ID := core.ID("span", traceID, "aaaaaaaaaaaaaaaa")
	span2ID := core.ID("span", traceID, "bbbbbbbbbbbbbbbb")
	err1ID := core.ID("error", "ERR_LATE")
	err2ID := core.ID("error", "ERR_EARLY")

	later := time.Date(2026, 1, 1, 12, 0, 1, 0, time.UTC)
	earlier := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	g := testutil.MakeGraph(
		[]core.Node{
			testutil.MakeNode(reqID, core.NodeRequest, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(span1ID, core.NodeSpan, map[string]any{"trace_id": traceID, "timestamp": later}),
			testutil.MakeNode(span2ID, core.NodeSpan, map[string]any{"trace_id": traceID, "timestamp": earlier}),
			testutil.MakeNode(err1ID, core.NodeError, map[string]any{"code": "ERR_LATE"}),
			testutil.MakeNode(err2ID, core.NodeError, map[string]any{"code": "ERR_EARLY"}),
		},
		[]core.Edge{
			{From: reqID, To: span1ID, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: span2ID, Type: core.EdgeRequestHasSpan},
			{From: span1ID, To: err1ID, Type: core.EdgeFailedWith},
			{From: span2ID, To: err2ID, Type: core.EdgeFailedWith},
		},
	)

	ex, err := ExplainRequest(g, reqID)
	if err != nil {
		t.Fatal(err)
	}

	if ex.ErrorCode != "ERR_EARLY" {
		t.Errorf("ErrorCode = %v, want ERR_EARLY (earlier timestamp wins tiebreak)", ex.ErrorCode)
	}
}

func TestExplainRequest_NoSpanErrorFallsBackToRequest(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	reqID := core.ID("request", traceID)
	spanID := core.ID("span", traceID, "aaaaaaaaaaaaaaaa")
	errID := core.ID("error", "REQ_ERR")

	g := testutil.MakeGraph(
		[]core.Node{
			testutil.MakeNode(reqID, core.NodeRequest, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(spanID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(errID, core.NodeError, map[string]any{"code": "REQ_ERR", "message": "request level error"}),
		},
		[]core.Edge{
			{From: reqID, To: spanID, Type: core.EdgeRequestHasSpan},
			// Span has no error edge — only request has the error
			{From: reqID, To: errID, Type: core.EdgeFailedWith},
		},
	)

	ex, err := ExplainRequest(g, reqID)
	if err != nil {
		t.Fatal(err)
	}

	if ex.ErrorCode != "REQ_ERR" {
		t.Errorf("ErrorCode = %v, want REQ_ERR", ex.ErrorCode)
	}
	if ex.SpanID != "" {
		t.Errorf("SpanID = %s, want empty (fallback to request-level error)", ex.SpanID)
	}
	if ex.SpanChain != nil {
		t.Errorf("SpanChain = %v, want nil", ex.SpanChain)
	}
}

func TestExplainRequest_CyclicSpanParentsNoPanic(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	reqID := core.ID("request", traceID)
	spanAID := core.ID("span", traceID, "aaaaaaaaaaaaaaaa")
	spanBID := core.ID("span", traceID, "bbbbbbbbbbbbbbbb")
	errID := core.ID("error", "CYCLE_ERR")

	g := testutil.MakeGraph(
		[]core.Node{
			testutil.MakeNode(reqID, core.NodeRequest, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(spanAID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(spanBID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(errID, core.NodeError, map[string]any{"code": "CYCLE_ERR", "message": "err"}),
		},
		[]core.Edge{
			{From: reqID, To: spanAID, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: spanBID, Type: core.EdgeRequestHasSpan},
			// Cycle: A → B → A
			{From: spanAID, To: spanBID, Type: core.EdgeSpanChildOf},
			{From: spanBID, To: spanAID, Type: core.EdgeSpanChildOf},
			{From: spanAID, To: errID, Type: core.EdgeFailedWith},
		},
	)

	// Must not panic or infinite-loop
	ex, err := ExplainRequest(g, reqID)
	if err != nil {
		t.Fatal(err)
	}
	if ex.ErrorCode != "CYCLE_ERR" {
		t.Errorf("ErrorCode = %v, want CYCLE_ERR", ex.ErrorCode)
	}
}

func TestExplainRequest_SameDepthNoTimestampDeterministic(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	reqID := core.ID("request", traceID)
	span1ID := core.ID("span", traceID, "aaaaaaaaaaaaaaaa")
	span2ID := core.ID("span", traceID, "bbbbbbbbbbbbbbbb")
	err1ID := core.ID("error", "ERR_1")
	err2ID := core.ID("error", "ERR_2")

	g := testutil.MakeGraph(
		[]core.Node{
			testutil.MakeNode(reqID, core.NodeRequest, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(span1ID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(span2ID, core.NodeSpan, map[string]any{"trace_id": traceID}),
			testutil.MakeNode(err1ID, core.NodeError, map[string]any{"code": "ERR_1"}),
			testutil.MakeNode(err2ID, core.NodeError, map[string]any{"code": "ERR_2"}),
		},
		[]core.Edge{
			{From: reqID, To: span1ID, Type: core.EdgeRequestHasSpan},
			{From: reqID, To: span2ID, Type: core.EdgeRequestHasSpan},
			{From: span1ID, To: err1ID, Type: core.EdgeFailedWith},
			{From: span2ID, To: err2ID, Type: core.EdgeFailedWith},
		},
	)

	// Run 20 times — must always pick the same winner (lexicographically smaller span ID)
	var first string
	for i := 0; i < 20; i++ {
		ex, err := ExplainRequest(g, reqID)
		if err != nil {
			t.Fatal(err)
		}
		code, _ := ex.ErrorCode.(string)
		if i == 0 {
			first = code
		} else if code != first {
			t.Fatalf("nondeterministic: iteration %d got %s, want %s", i, code, first)
		}
	}
}
