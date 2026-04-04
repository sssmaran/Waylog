package tracestore

import "testing"

func TestBuildTree_SingleRoot(t *testing.T) {
	spans := []SpanRecord{
		{SpanID: "root", ParentSpanID: "", Service: "gw"},
		{SpanID: "child1", ParentSpanID: "root", Service: "checkout"},
		{SpanID: "child2", ParentSpanID: "root", Service: "payment"},
	}
	roots := BuildTree(spans)
	if len(roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(roots))
	}
	if roots[0].Span.SpanID != "root" {
		t.Errorf("root SpanID=%q", roots[0].Span.SpanID)
	}
	if len(roots[0].Children) != 2 {
		t.Errorf("root has %d children, want 2", len(roots[0].Children))
	}
}

func TestBuildTree_OrphanBecomesRoot(t *testing.T) {
	spans := []SpanRecord{
		{SpanID: "child", ParentSpanID: "missing-parent", Service: "svc"},
	}
	roots := BuildTree(spans)
	if len(roots) != 1 {
		t.Fatalf("orphan should become root, got %d roots", len(roots))
	}
	if roots[0].Span.SpanID != "child" {
		t.Errorf("got SpanID=%q", roots[0].Span.SpanID)
	}
}

func TestBuildTree_EmptySpans(t *testing.T) {
	roots := BuildTree(nil)
	if len(roots) != 0 {
		t.Errorf("expected 0 roots for nil spans, got %d", len(roots))
	}
}

func TestBuildTree_CycleDetection(t *testing.T) {
	spans := []SpanRecord{
		{SpanID: "a", ParentSpanID: "b", Service: "svc1"},
		{SpanID: "b", ParentSpanID: "a", Service: "svc2"},
	}
	roots := BuildTree(spans)
	if len(roots) == 0 {
		t.Error("expected at least one root from cyclic spans")
	}
}

func TestBuildTree_DeepChain(t *testing.T) {
	spans := []SpanRecord{
		{SpanID: "r", ParentSpanID: "", Service: "gw"},
		{SpanID: "c1", ParentSpanID: "r", Service: "auth"},
		{SpanID: "c2", ParentSpanID: "c1", Service: "db"},
	}
	roots := BuildTree(spans)
	if len(roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(roots))
	}
	if len(roots[0].Children) != 1 {
		t.Fatalf("root children=%d, want 1", len(roots[0].Children))
	}
	if len(roots[0].Children[0].Children) != 1 {
		t.Fatalf("depth-2 children=%d, want 1", len(roots[0].Children[0].Children))
	}
	if roots[0].Children[0].Children[0].Span.Service != "db" {
		t.Error("deepest node should be db")
	}
}
