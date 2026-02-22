package store

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestStore_Merge_RequestDeterministicMerge(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"

	// Child event arrives first (non-root)
	childEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("pppppppppppppppp"),
		testutil.WithService("payment-demo"),
		testutil.WithFlow("payment"),
		testutil.WithStatusCode(502),
		testutil.WithLatency(12),
		testutil.WithError("PMT_502", "payment failed"),
		testutil.WithEventName("payment-demo.error"),
	)
	s.Merge(b.Build(childEv))

	// Root event arrives second
	rootEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("pppppppppppppppp"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithFlow("purchase"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(rootEv))

	// Verify request node
	snap := s.Snapshot()
	reqID := core.ID("request", traceID)
	req, ok := snap.Nodes[reqID]
	if !ok {
		t.Fatalf("request node %s not found", reqID)
	}

	// Root's values should win for status_code, latency_ms, event_name
	if got := req.Attr["status_code"]; got != 200 {
		t.Errorf("status_code = %v, want 200 (from root)", got)
	}
	if got := req.Attr["latency_ms"]; got != int64(45) {
		t.Errorf("latency_ms = %v, want 45 (from root)", got)
	}
	if got := req.Attr["event_name"]; got != "api-gateway.request" {
		t.Errorf("event_name = %v, want api-gateway.request (from root)", got)
	}
	if got := req.Attr["flow"]; got != "purchase" {
		t.Errorf("flow = %v, want purchase (from root)", got)
	}

	// success should be AND: child was false, so overall false
	if got := req.Attr["success"]; got != false {
		t.Errorf("success = %v, want false (AND of child failure)", got)
	}

	// is_root should be true (root event set it)
	if got := req.Attr["is_root"]; got != true {
		t.Errorf("is_root = %v, want true", got)
	}

	codes, ok := req.Attr["error_codes"].([]string)
	if !ok {
		t.Fatalf("error_codes should be []string, got %T (%v)", req.Attr["error_codes"], req.Attr["error_codes"])
	}
	if len(codes) != 1 || codes[0] != "PMT_502" {
		t.Errorf("error_codes = %v, want [PMT_502]", codes)
	}
}

func TestStore_Merge_SpanStubEnrichment(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	parentSpanID := "1111111111111111"
	childSpanID := "2222222222222222"

	// Child event arrives first — creates a stub for parent span
	childEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(childSpanID),
		testutil.WithParentSpanID(parentSpanID),
		testutil.WithService("checkout-demo"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(32),
	)
	s.Merge(b.Build(childEv))

	// Verify parent span is a stub (missing enriched fields)
	snap := s.Snapshot()
	parentNodeID := core.ID("span", traceID, parentSpanID)
	parentNode, ok := snap.Nodes[parentNodeID]
	if !ok {
		t.Fatalf("parent span stub %s not found", parentNodeID)
	}
	if _, has := parentNode.Attr["status_code"]; has {
		t.Error("parent stub should not have status_code yet")
	}

	// Now parent's own event arrives — should enrich the stub
	parentEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(parentSpanID),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(parentEv))

	snap = s.Snapshot()
	parentNode = snap.Nodes[parentNodeID]

	// Verify enriched attrs
	if got := parentNode.Attr["service"]; got != "api-gateway" {
		t.Errorf("service = %v, want api-gateway", got)
	}
	if got := parentNode.Attr["status_code"]; got != 200 {
		t.Errorf("status_code = %v, want 200", got)
	}
	if got := parentNode.Attr["latency_ms"]; got != int64(45) {
		t.Errorf("latency_ms = %v, want 45", got)
	}
	if got := parentNode.Attr["event_name"]; got != "api-gateway.request" {
		t.Errorf("event_name = %v, want api-gateway.request", got)
	}
}

func TestStore_EdgeDedup(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "cccccccccccccccccccccccccccccccc"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
	)

	g := b.Build(ev)
	s.Merge(g)
	edgeCount1 := len(s.Snapshot().Edges)

	// Merge the same graph again — edge count should not increase
	s.Merge(g)
	edgeCount2 := len(s.Snapshot().Edges)

	if edgeCount2 != edgeCount1 {
		t.Errorf("edge count increased from %d to %d after duplicate merge", edgeCount1, edgeCount2)
	}
}

func TestStore_Index_TraceToRequest(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "dddddddddddddddddddddddddddddddd"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
	)
	s.Merge(b.Build(ev))

	reqID, ok := s.RequestIDForTrace(traceID)
	if !ok {
		t.Fatal("expected RequestIDForTrace to return true")
	}
	expectedReqID := core.ID("request", traceID)
	if reqID != expectedReqID {
		t.Errorf("RequestIDForTrace = %s, want %s", reqID, expectedReqID)
	}

	// Unknown trace
	_, ok = s.RequestIDForTrace("0000000000000000000000000000000f")
	if ok {
		t.Error("expected RequestIDForTrace to return false for unknown trace")
	}
}

func TestStore_Index_TraceToSpans(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	// Two events for the same trace with different spans
	ev1 := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
	)
	ev2 := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("2222222222222222"),
		testutil.WithParentSpanID("1111111111111111"),
		testutil.WithService("checkout-demo"),
	)

	s.Merge(b.Build(ev1))
	s.Merge(b.Build(ev2))
	s.Merge(b.Build(ev2)) // duplicate merge should not duplicate span IDs in index

	spanIDs := s.SpanIDsForTrace(traceID)
	// ev1 creates span 1111, ev2 creates span 2222 + stub for parent 1111.
	// Index should contain unique IDs only.
	if len(spanIDs) != 2 {
		t.Errorf("expected exactly 2 unique span IDs for trace, got %d: %v", len(spanIDs), spanIDs)
	}

	// Verify the span IDs contain our expected spans
	expected := map[string]bool{
		core.ID("span", traceID, "1111111111111111"): false,
		core.ID("span", traceID, "2222222222222222"): false,
	}
	for _, id := range spanIDs {
		if _, ok := expected[id]; ok {
			expected[id] = true
		}
	}
	for id, found := range expected {
		if !found {
			t.Errorf("expected span ID %s not found in index", id)
		}
	}
}

func TestStore_Index_SpanIDsForTrace_ReturnsCopy(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "abababababababababababababababab"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("3333333333333333"),
		testutil.WithService("api-gateway"),
	)
	s.Merge(b.Build(ev))

	got := s.SpanIDsForTrace(traceID)
	if len(got) == 0 {
		t.Fatal("expected at least one span ID")
	}
	got[0] = "mutated-by-caller"

	fresh := s.SpanIDsForTrace(traceID)
	if len(fresh) == 0 {
		t.Fatal("expected at least one span ID on second read")
	}
	if fresh[0] == "mutated-by-caller" {
		t.Fatal("SpanIDsForTrace returned internal backing slice; caller mutation leaked into store")
	}
}

func TestStore_Merge_RecomputesFacts(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa02"

	// First event: checkout service, no error
	ev1 := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithParentSpanID(""),
		testutil.WithService("checkout"),
		testutil.WithStatusCode(200),
		testutil.WithEventName("checkout.request"),
	)
	s.Merge(b.Build(ev1))

	// Verify no errors in counters yet
	if len(s.counters.ErrorCount) != 0 {
		t.Fatalf("expected 0 errors after first merge, got %v", s.counters.ErrorCount)
	}

	// Second event: same trace, child span with an error
	ev2 := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("2222222222222222"),
		testutil.WithParentSpanID("1111111111111111"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(500),
		testutil.WithError("ERR_PAYMENT", "payment failed"),
		testutil.WithEventName("payment.error"),
	)
	s.Merge(b.Build(ev2))

	// The error node should now be counted
	errNodeID := core.ID("error", "ERR_PAYMENT")
	count, ok := s.counters.ErrorCount[errNodeID]
	if !ok || count < 1 {
		t.Errorf("expected ErrorCount[%s] >= 1, got %d (ok=%v); full counters: %v",
			errNodeID, count, ok, s.counters.ErrorCount)
	}
}

func TestStore_Index_Restore_Rebuilds(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "ffffffffffffffffffffffffffffffff"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithService("api-gateway"),
	)
	s.Merge(b.Build(ev))

	// Take snapshot and restore into a fresh store
	snap := s.Snapshot()
	s2 := NewStore()
	s2.Restore(snap)

	// Indexes should be rebuilt from restored graph
	reqID, ok := s2.RequestIDForTrace(traceID)
	if !ok {
		t.Fatal("RequestIDForTrace should work after Restore")
	}
	if reqID != core.ID("request", traceID) {
		t.Errorf("RequestIDForTrace = %s, want %s", reqID, core.ID("request", traceID))
	}

	spanIDs := s2.SpanIDsForTrace(traceID)
	if len(spanIDs) == 0 {
		t.Error("SpanIDsForTrace should return spans after Restore")
	}

	// Edge dedup should also work after restore — merge same snapshot again
	edgesBefore := len(s2.Snapshot().Edges)
	s2.Merge(snap)
	edgesAfter := len(s2.Snapshot().Edges)
	if edgesAfter != edgesBefore {
		t.Errorf("edges grew from %d to %d after duplicate merge post-Restore", edgesBefore, edgesAfter)
	}
}

func TestStore_LateRootMerge_UpdatesRootService(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa03"
	reqID := core.ID("request", traceID)

	// Child span arrives first — same service set as root will use,
	// so Services/Errors/Flags don't change when root merges.
	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("2222222222222222"),
		testutil.WithParentSpanID("1111111111111111"),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(child))

	// Before root: RootService should be empty.
	facts1, ok := s.requestFacts[reqID]
	if !ok {
		t.Fatal("requestFacts not found after child merge")
	}
	if facts1.RootService != "" {
		t.Errorf("RootService = %q before root merge, want empty", facts1.RootService)
	}

	// Root span arrives — counter-relevant fields (Services/Errors/Flags) unchanged.
	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(root))

	// After root: RootService must be set even though counters didn't change.
	facts2, ok := s.requestFacts[reqID]
	if !ok {
		t.Fatal("requestFacts not found after root merge")
	}
	if facts2.RootService != "api-gateway" {
		t.Errorf("RootService = %q after root merge, want api-gateway", facts2.RootService)
	}

	// Counter-relevant fields should be identical (no spurious recompute).
	if !factsEqual(facts1, facts2) {
		t.Error("counter-relevant fields changed unexpectedly")
	}
}
