package store

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// mergeInto runs events through the canonical ingest pipeline: builder → store
// merge + trace-store upsert. Mirrors internal/ingest/handler.go.
func mergeInto(s *Store, ts *tracestore.Store, b *build.Builder, events ...event.WideEvent) {
	for _, ev := range events {
		res := b.BuildResult(ev)
		s.Merge(res.Graph)
		if res.Span != nil && ts != nil {
			ts.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), res.Span)
		}
	}
}

// renderTree produces a stable deterministic rendering of a span forest.
func renderTree(nodes []*tracestore.TreeNode) string {
	var lines []string
	var walk func(*tracestore.TreeNode, int)
	walk = func(n *tracestore.TreeNode, depth int) {
		if n == nil {
			return
		}
		lines = append(lines, fmt.Sprintf("%s%s:%s", strings.Repeat("  ", depth), n.Span.SpanID, n.Span.Service))
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, r := range nodes {
		walk(r, 0)
	}
	return strings.Join(lines, "\n")
}

// Invariant 1: Out-of-order span arrival yields the same tree as in-order arrival.
func TestInvariant_OutOfOrderArrival_SameTree(t *testing.T) {
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	base := time.Now().UTC()

	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithParentSpanID(""),
		testutil.WithService("gateway"),
		testutil.WithEventName("gateway.request"),
		testutil.WithTimestamp(base),
	)
	mid := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("mmmmmmmmmmmmmmmm"),
		testutil.WithParentSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithService("checkout"),
		testutil.WithEventName("checkout.request"),
		testutil.WithTimestamp(base.Add(time.Millisecond)),
	)
	leaf := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("llllllllllllllll"),
		testutil.WithParentSpanID("mmmmmmmmmmmmmmmm"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "downstream failed"),
		testutil.WithTimestamp(base.Add(2*time.Millisecond)),
	)

	inOrder := buildForest(traceID, root, mid, leaf)
	outOfOrder := buildForest(traceID, leaf, mid, root)

	want := renderTree(inOrder)
	got := renderTree(outOfOrder)
	if want != got {
		t.Fatalf("tree diverges on out-of-order arrival\nin-order:\n%s\nout-of-order:\n%s", want, got)
	}
}

func buildForest(traceID string, events ...event.WideEvent) []*tracestore.TreeNode {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	mergeInto(s, ts, b, events...)
	rec, ok := ts.Get(traceID)
	if !ok {
		return nil
	}
	return tracestore.BuildTree(rec.Spans)
}

// Invariant 2: Root arrives late → root_service is upgraded on the request facts.
func TestInvariant_RootLateMerge_UpgradesRootService(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"

	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "failed"),
	)
	mergeInto(s, ts, b, child)

	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithParentSpanID(""),
		testutil.WithService("gateway"),
		testutil.WithEventName("gateway.request"),
		testutil.WithStatusCode(200),
	)
	mergeInto(s, ts, b, root)

	facts, ok := s.requestFacts[core.ID("request", traceID)]
	if !ok {
		t.Fatalf("request facts missing")
	}
	if facts.RootService != "gateway" {
		t.Fatalf("root_service = %q, want %q (late root must win)", facts.RootService, "gateway")
	}
}

// Invariant 3: Fan-out children all appear in the tree, and the error index
// counts one entry per failed request (not per propagated hop).
func TestInvariant_FanOut_ChildrenAllAppear_BlastRadiusStable(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3"

	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithParentSpanID(""),
		testutil.WithService("gateway"),
	)
	left := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("lllllllllllllll1"),
		testutil.WithParentSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithService("checkout"),
	)
	right := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("rrrrrrrrrrrrrrr2"),
		testutil.WithParentSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithService("inventory"),
		testutil.WithStatusCode(500),
		testutil.WithError("INV_500", "stock fetch failed"),
	)
	mergeInto(s, ts, b, root, left, right)

	rec, ok := ts.Get(traceID)
	if !ok {
		t.Fatalf("trace record missing")
	}
	roots := tracestore.BuildTree(rec.Spans)
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(roots))
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("expected 2 fan-out children, got %d", len(roots[0].Children))
	}

	ids, _ := s.ErrorIndex("INV_500")
	if len(ids) != 1 {
		t.Fatalf("error-index request count = %d, want 1 (blast_radius must not inflate by hop)", len(ids))
	}
}

// Invariant 4: Retries land on separate request nodes and carry retry.of +
// retry.previous_attempt_id so UIs can render them as sibling attempts.
func TestInvariant_Retries_RenderAsSiblingAttempts(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()

	firstTrace := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa4"
	retryTrace := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaab4"

	first := testutil.MakeEvent(
		testutil.WithTraceID(firstTrace),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "upstream reset"),
	)
	retry := testutil.MakeEvent(
		testutil.WithTraceID(retryTrace),
		testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(200),
		testutil.WithRetry(1, firstTrace),
	)
	mergeInto(s, ts, b, first, retry)

	snap := s.Snapshot()
	retryReq, ok := snap.Nodes[core.ID("request", retryTrace)]
	if !ok {
		t.Fatalf("retry request node missing")
	}
	if got := retryReq.Attr["retry_of"]; got != 1 {
		t.Fatalf("retry_of = %v, want 1", got)
	}
	if got, _ := retryReq.Attr["retry_previous_attempt_id"].(string); got != firstTrace {
		t.Fatalf("retry_previous_attempt_id = %q, want %q", got, firstTrace)
	}
	if _, ok := snap.Nodes[core.ID("request", firstTrace)]; !ok {
		t.Fatalf("original request node missing — retries must not replace the original")
	}
}

// Invariant 5: Duplicate-span ingestion does not double-render the span.
func TestInvariant_DuplicateSpan_NoDoubleRender(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa5"

	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithParentSpanID(""),
		testutil.WithService("gateway"),
	)
	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("rrrrrrrrrrrrrrrr"),
		testutil.WithService("checkout"),
	)
	mergeInto(s, ts, b, root, child, child)

	rec, ok := ts.Get(traceID)
	if !ok {
		t.Fatalf("trace record missing")
	}
	if len(rec.Spans) != 2 {
		t.Fatalf("duplicate span was not deduped: got %d, want 2", len(rec.Spans))
	}
	roots := tracestore.BuildTree(rec.Spans)
	if len(roots) != 1 || len(roots[0].Children) != 1 {
		t.Fatalf("duplicate produced extra tree entry: %s", renderTree(roots))
	}
}

// Invariant 6: parent_request_id is stored on the child request node only and
// does NOT attach the child span as a span_child_of the parent trace.
func TestInvariant_ParentRequestID_RendersAsSecondaryLink(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	parentTrace := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa6"
	childTrace := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	parent := testutil.MakeEvent(
		testutil.WithTraceID(parentTrace),
		testutil.WithSpanID("pppppppppppppppp"),
		testutil.WithService("gateway"),
	)
	child := testutil.MakeEvent(
		testutil.WithTraceID(childTrace),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithService("worker"),
		testutil.WithParentRequestID(core.ID("request", parentTrace)),
	)
	mergeInto(s, ts, b, parent, child)

	snap := s.Snapshot()
	childReq := snap.Nodes[core.ID("request", childTrace)]
	got, _ := childReq.Attr["parent_request_id"].(string)
	if want := core.ID("request", parentTrace); got != want {
		t.Fatalf("parent_request_id = %q, want %q", got, want)
	}

	for _, e := range snap.Edges {
		if e.Type == core.EdgeSpanChildOf && e.From == "cccccccccccccccc" {
			t.Fatalf("child span was linked as span_child_of parent trace — must remain a secondary reference only")
		}
	}
}

// Invariant 7: A child whose parent span never arrived must not crash tree
// rendering; orphans are promoted to roots.
func TestInvariant_MissingParent_TreeStillRenders(t *testing.T) {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa7"

	orphan := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("oooooooooooooooo"),
		testutil.WithParentSpanID("nonexistentparent"),
		testutil.WithService("payment"),
	)
	mergeInto(s, ts, b, orphan)

	rec, ok := ts.Get(traceID)
	if !ok {
		t.Fatalf("trace record missing")
	}
	roots := tracestore.BuildTree(rec.Spans)
	if len(roots) != 1 || roots[0].Span.SpanID != "oooooooooooooooo" {
		t.Fatalf("orphan was not promoted to root: %s", renderTree(roots))
	}
}

// Invariant 8: Events with differing event_name shapes (native vs OTLP-style)
// produce the same explain-style error-code set for the trace.
func TestInvariant_MixedEventNames_StableExplainOutput(t *testing.T) {
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa8"

	native := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("nnnnnnnnnnnnnnnn"),
		testutil.WithService("payment"),
		testutil.WithEventName("payment.request"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "downstream failed"),
	)
	otlpShaped := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("nnnnnnnnnnnnnnnn"),
		testutil.WithService("payment"),
		testutil.WithEventName("POST /charge"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "downstream failed"),
	)

	a := explainCodes(traceID, native)
	b := explainCodes(traceID, otlpShaped)
	if !equalStrings(a, b) {
		t.Fatalf("explain diverged across event_name shapes: native=%v otlp=%v", a, b)
	}
}

func explainCodes(traceID string, ev event.WideEvent) []string {
	s := NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	mergeInto(s, ts, b, ev)
	facts, ok := s.requestFacts[core.ID("request", traceID)]
	if !ok {
		return nil
	}
	codes := append([]string(nil), facts.Errors...)
	sort.Strings(codes)
	return codes
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
