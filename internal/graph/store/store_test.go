package store

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func newStoreWithTraceStore() (*Store, *tracestore.Store) {
	return NewStore(), tracestore.NewStore()
}

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

func TestStore_TraceStore_SpanMergeBySpanID(t *testing.T) {
	s, ts := newStoreWithTraceStore()
	b := build.NewBuilder()
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	parentSpanID := "1111111111111111"
	childSpanID := "2222222222222222"

	childEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(childSpanID),
		testutil.WithParentSpanID(parentSpanID),
		testutil.WithService("checkout-demo"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(32),
	)
	childResult := b.BuildResult(childEv)
	s.Merge(childResult.Graph)
	if childResult.Span != nil {
		ts.Upsert(traceID, core.ID("request", traceID), childResult.Span)
	}

	parentEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(parentSpanID),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithEventName("api-gateway.request"),
	)
	parentResult := b.BuildResult(parentEv)
	s.Merge(parentResult.Graph)
	if parentResult.Span != nil {
		ts.Upsert(traceID, core.ID("request", traceID), parentResult.Span)
	}

	rec, ok := ts.Get(traceID)
	if !ok {
		t.Fatalf("trace %s not found", traceID)
	}
	if len(rec.Spans) != 2 {
		t.Fatalf("expected 2 span records, got %d", len(rec.Spans))
	}
	var parent tracestore.SpanRecord
	found := false
	for _, span := range rec.Spans {
		if span.SpanID == parentSpanID {
			parent = span
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("parent span %s not found", parentSpanID)
	}
	if parent.Service != "api-gateway" {
		t.Errorf("service = %v, want api-gateway", parent.Service)
	}
	if parent.StatusCode != 200 {
		t.Errorf("status_code = %v, want 200", parent.StatusCode)
	}
	if parent.LatencyMs != int64(45) {
		t.Errorf("latency_ms = %v, want 45", parent.LatencyMs)
	}
	if parent.EventName != "api-gateway.request" {
		t.Errorf("event_name = %v, want api-gateway.request", parent.EventName)
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

func TestStore_TraceStore_Get_ReturnsUniqueSpans(t *testing.T) {
	s, ts := newStoreWithTraceStore()
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

	for _, current := range []struct {
		traceID string
		event   event.WideEvent
	}{
		{traceID: traceID, event: ev1},
		{traceID: traceID, event: ev2},
		{traceID: traceID, event: ev2},
	} {
		result := b.BuildResult(current.event)
		s.Merge(result.Graph)
		if result.Span != nil {
			ts.Upsert(current.traceID, core.ID("request", current.traceID), result.Span)
		}
	}

	rec, ok := ts.Get(traceID)
	if !ok {
		t.Fatal("expected trace record to exist")
	}
	if len(rec.Spans) != 2 {
		t.Fatalf("expected 2 unique span records, got %d", len(rec.Spans))
	}
}

func TestStore_TraceStore_Get_ReturnsCopy(t *testing.T) {
	s, ts := newStoreWithTraceStore()
	b := build.NewBuilder()
	traceID := "abababababababababababababababab"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("3333333333333333"),
		testutil.WithService("api-gateway"),
	)
	result := b.BuildResult(ev)
	s.Merge(result.Graph)
	if result.Span != nil {
		ts.Upsert(traceID, core.ID("request", traceID), result.Span)
	}
	got, ok := ts.Get(traceID)
	if !ok || len(got.Spans) == 0 {
		t.Fatal("expected at least one span record")
	}
	got.Spans[0].SpanID = "mutated-by-caller"

	fresh, ok := ts.Get(traceID)
	if !ok || len(fresh.Spans) == 0 {
		t.Fatal("expected at least one span record on second read")
	}
	if fresh.Spans[0].SpanID == "mutated-by-caller" {
		t.Fatal("Get returned internal backing data; caller mutation leaked into store")
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

	// The error code should now be counted (keyed by code string, not node ID).
	count, ok := s.counters.ErrorCount["ERR_PAYMENT"]
	if !ok || count < 1 {
		t.Errorf("expected ErrorCount[ERR_PAYMENT] >= 1, got %d (ok=%v); full counters: %v",
			count, ok, s.counters.ErrorCount)
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

func TestStore_Merge_RootOverwritesHTTPMethodAndRouteTemplate(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa04"
	reqID := core.ID("request", traceID)

	// Child arrives first with method/template.
	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("2222222222222222"),
		testutil.WithParentSpanID("1111111111111111"),
		testutil.WithService("payment"),
		testutil.WithEventName("payment.request"),
		testutil.WithHTTPMethod("GET"),
		testutil.WithRouteTemplate("/payments/{id}"),
	)
	s.Merge(b.Build(child))

	snap := s.Snapshot()
	req := snap.Nodes[reqID]
	if got := req.Attr["http_method"]; got != "GET" {
		t.Fatalf("http_method = %v, want GET before root merge", got)
	}
	if got := req.Attr["route_template"]; got != "/payments/{id}" {
		t.Fatalf("route_template = %v, want /payments/{id} before root merge", got)
	}

	// Root arrives later with new method/template and should overwrite.
	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithService("api-gateway"),
		testutil.WithEventName("api-gateway.request"),
		testutil.WithHTTPMethod("POST"),
		testutil.WithRouteTemplate("/checkout"),
	)
	s.Merge(b.Build(root))

	snap = s.Snapshot()
	req = snap.Nodes[reqID]
	if got := req.Attr["http_method"]; got != "POST" {
		t.Errorf("http_method = %v, want POST after root merge", got)
	}
	if got := req.Attr["route_template"]; got != "/checkout" {
		t.Errorf("route_template = %v, want /checkout after root merge", got)
	}
}

// ---------- ErrorIndex tests ----------

func requireSetEqual(t *testing.T, label string, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len = %d, want %d; got %v", label, len(got), len(want), got)
	}
	set := map[string]struct{}{}
	for _, v := range want {
		set[v] = struct{}{}
	}
	for _, v := range got {
		if _, ok := set[v]; !ok {
			t.Fatalf("%s: unexpected element %q; got %v, want %v", label, v, got, want)
		}
	}
}

func TestErrorIndex_UnknownCode(t *testing.T) {
	s := NewStore()
	ids, ready := s.ErrorIndex("NOPE")
	if !ready {
		t.Fatal("expected ready=true on fresh store")
	}
	if len(ids) != 0 {
		t.Errorf("expected empty slice, got %v", ids)
	}
}

func TestErrorIndex_BasicLookup(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aa00000000000000000000000000aa01"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1100000000000001"),
		testutil.WithService("svc-a"),
		testutil.WithStatusCode(500),
		testutil.WithError("SVC_500", "internal error"),
		testutil.WithEventName("svc-a.error"),
	)
	s.Merge(b.Build(ev))

	ids, ready := s.ErrorIndex("SVC_500")
	if !ready {
		t.Fatal("expected ready=true")
	}
	requireSetEqual(t, "ErrorIndex(SVC_500)", ids, []string{core.ID("request", traceID)})
}

func TestErrorIndex_DeduplicatesSameRequest_DuplicateEdges(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aa00000000000000000000000000aa02"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1100000000000002"),
		testutil.WithService("svc-b"),
		testutil.WithStatusCode(502),
		testutil.WithError("DUP_502", "bad gateway"),
		testutil.WithEventName("svc-b.error"),
	)
	g := b.Build(ev)
	s.Merge(g)
	s.Merge(g) // duplicate merge

	ids, ready := s.ErrorIndex("DUP_502")
	if !ready {
		t.Fatal("expected ready=true")
	}
	requireSetEqual(t, "ErrorIndex(DUP_502)", ids, []string{core.ID("request", traceID)})
}

func TestErrorIndex_DeduplicatesSameRequest_SpanAndRequestOrigin(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aa00000000000000000000000000aa03"

	// Root span with error
	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1100000000000003"),
		testutil.WithParentSpanID(""),
		testutil.WithService("svc-c"),
		testutil.WithStatusCode(500),
		testutil.WithError("BOTH_500", "root error"),
		testutil.WithEventName("svc-c.error"),
	)
	s.Merge(b.Build(root))

	// Child span with same error code
	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1100000000000004"),
		testutil.WithParentSpanID("1100000000000003"),
		testutil.WithService("svc-c"),
		testutil.WithStatusCode(500),
		testutil.WithError("BOTH_500", "child error"),
		testutil.WithEventName("svc-c.error"),
	)
	s.Merge(b.Build(child))

	ids, ready := s.ErrorIndex("BOTH_500")
	if !ready {
		t.Fatal("expected ready=true")
	}
	requireSetEqual(t, "ErrorIndex(BOTH_500)", ids, []string{core.ID("request", traceID)})
}

func TestErrorIndex_MultiRequestSameCode(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceA := "aa00000000000000000000000000aa04"
	traceB := "aa00000000000000000000000000aa05"

	evA := testutil.MakeEvent(
		testutil.WithTraceID(traceA),
		testutil.WithSpanID("1100000000000005"),
		testutil.WithService("svc-d"),
		testutil.WithStatusCode(503),
		testutil.WithError("SHARED_503", "service unavailable"),
		testutil.WithEventName("svc-d.error"),
	)
	evB := testutil.MakeEvent(
		testutil.WithTraceID(traceB),
		testutil.WithSpanID("1100000000000006"),
		testutil.WithService("svc-e"),
		testutil.WithStatusCode(503),
		testutil.WithError("SHARED_503", "service unavailable"),
		testutil.WithEventName("svc-e.error"),
	)
	s.Merge(b.Build(evA))
	s.Merge(b.Build(evB))

	ids, ready := s.ErrorIndex("SHARED_503")
	if !ready {
		t.Fatal("expected ready=true")
	}
	requireSetEqual(t, "ErrorIndex(SHARED_503)", ids, []string{
		core.ID("request", traceA),
		core.ID("request", traceB),
	})
}

func TestErrorIndex_ReadinessAfterRestore(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aa00000000000000000000000000aa06"

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("1100000000000007"),
		testutil.WithService("svc-f"),
		testutil.WithStatusCode(500),
		testutil.WithError("RESTORE_ERR", "fail"),
		testutil.WithEventName("svc-f.error"),
	)
	s.Merge(b.Build(ev))

	snap := s.Snapshot()

	s2 := NewStore()
	s2.Restore(snap)

	ids, ready := s2.ErrorIndex("RESTORE_ERR")
	if !ready {
		t.Fatal("expected ready=true after Restore")
	}
	requireSetEqual(t, "ErrorIndex(RESTORE_ERR) after restore", ids, []string{core.ID("request", traceID)})
}

func TestErrorIndex_PruneRemovesStaleEntries(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()

	// Use a fixed reference time to avoid any time.Now() sensitivity.
	ref := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	oldTrace := "aa00000000000000000000000000aa07"
	newTrace := "aa00000000000000000000000000aa08"

	oldEv := testutil.MakeEvent(
		testutil.WithTraceID(oldTrace),
		testutil.WithSpanID("1100000000000008"),
		testutil.WithService("svc-old"),
		testutil.WithStatusCode(500),
		testutil.WithError("OLD_ERR", "old failure"),
		testutil.WithEventName("svc-old.error"),
		testutil.WithTimestamp(ref.Add(-2*time.Hour)),
		testutil.WithUser("user-old", "standard", "us-west-2"),
	)
	newEv := testutil.MakeEvent(
		testutil.WithTraceID(newTrace),
		testutil.WithSpanID("1100000000000009"),
		testutil.WithService("svc-new"),
		testutil.WithStatusCode(500),
		testutil.WithError("NEW_ERR", "new failure"),
		testutil.WithEventName("svc-new.error"),
		testutil.WithTimestamp(ref),
		testutil.WithUser("user-new", "standard", "us-west-2"),
	)

	s.Merge(b.Build(oldEv))
	s.Merge(b.Build(newEv))

	// Prune with 1h cutoff — old entries should be removed
	s.PruneOlderThan(ref.Add(-1 * time.Hour))

	oldIDs, ready := s.ErrorIndex("OLD_ERR")
	if !ready {
		t.Fatal("expected ready=true after prune")
	}
	if len(oldIDs) != 0 {
		t.Errorf("OLD_ERR should be empty after prune, got %v", oldIDs)
	}

	newIDs, ready := s.ErrorIndex("NEW_ERR")
	if !ready {
		t.Fatal("expected ready=true after prune")
	}
	requireSetEqual(t, "ErrorIndex(NEW_ERR) after prune", newIDs, []string{core.ID("request", newTrace)})
}

func TestErrorIndex_SpanToRequestResolution(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aa00000000000000000000000000aa09"

	// Root span (no error) — creates request + span nodes
	root := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("110000000000000a"),
		testutil.WithParentSpanID(""),
		testutil.WithService("svc-g"),
		testutil.WithStatusCode(200),
		testutil.WithEventName("svc-g.request"),
	)
	s.Merge(b.Build(root))

	// Child span with error — FailedWith edge from span node to error node
	child := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("110000000000000b"),
		testutil.WithParentSpanID("110000000000000a"),
		testutil.WithService("svc-h"),
		testutil.WithStatusCode(500),
		testutil.WithError("CHILD_ERR", "child failed"),
		testutil.WithEventName("svc-h.error"),
	)
	s.Merge(b.Build(child))

	ids, ready := s.ErrorIndex("CHILD_ERR")
	if !ready {
		t.Fatal("expected ready=true")
	}
	requireSetEqual(t, "ErrorIndex(CHILD_ERR) via span→request", ids, []string{core.ID("request", traceID)})
}
