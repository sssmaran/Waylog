package analysis

import (
	"fmt"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// ingestCascade emits a payment → checkout → api-gateway failing cascade for a
// single trace and returns the trace id. Each span carries a different error
// code; the root cause is PMT_502 (leaf).
func ingestCascade(t *testing.T, s *store.Store, ts *tracestore.Store, b *build.Builder, idx int, when time.Time) string {
	t.Helper()
	traceID := fmt.Sprintf("cccc%028d", idx)
	reqID := core.ID("request", traceID)

	ingest := func(ev event.WideEvent) {
		r := b.BuildResult(ev)
		s.Merge(r.Graph)
		if ts != nil && r.Span != nil {
			ts.Upsert(traceID, reqID, r.Span)
		}
	}

	// payment (leaf — deepest failing span)
	ingest(testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(fmt.Sprintf("p%015d", idx)),
		testutil.WithParentSpanID(fmt.Sprintf("c%015d", idx)),
		testutil.WithService("payment"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "payment failed"),
		testutil.WithCallerService("checkout"),
		testutil.WithTimestamp(when),
	))
	// checkout (middle)
	ingest(testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(fmt.Sprintf("c%015d", idx)),
		testutil.WithParentSpanID(fmt.Sprintf("a%015d", idx)),
		testutil.WithService("checkout"),
		testutil.WithStatusCode(502),
		testutil.WithError("CHK_DOWNSTREAM", "downstream failed"),
		testutil.WithCallerService("api-gateway"),
		testutil.WithTimestamp(when.Add(1*time.Millisecond)),
	))
	// api-gateway (root)
	ingest(testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(fmt.Sprintf("a%015d", idx)),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(502),
		testutil.WithError("GW_DOWNSTREAM", "downstream failed"),
		testutil.WithTimestamp(when.Add(2*time.Millisecond)),
	))
	return traceID
}

// TestRollupWindow_RootCauseCounted_PMT502IsThreeNotNine is the canonical
// regression test for the root-cause aggregation bug. Three cascading
// failures payment→checkout→api-gateway previously reported each code with
// count=9 (3 requests × 3 propagated codes). The correct behavior counts the
// deepest failing span once per request, so PMT_502 = 3 and the propagation
// codes do not appear in the primary rollup.
func TestRollupWindow_RootCauseCounted_PMT502IsThreeNotNine(t *testing.T) {
	s := store.NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	now := time.Now().UTC()

	for i := range 3 {
		ingestCascade(t, s, ts, b, i, now.Add(-20*time.Second))
	}

	summary := RollupWindow(graphOf(s), s, ts, now.Add(-time.Minute), now.Add(time.Minute))

	if summary.TotalRequests != 3 {
		t.Errorf("TotalRequests = %d, want 3", summary.TotalRequests)
	}
	if summary.TotalFailures != 3 {
		t.Errorf("TotalFailures = %d, want 3", summary.TotalFailures)
	}
	if got := summary.PrimaryErrorCount["PMT_502"]; got != 3 {
		t.Errorf("PrimaryErrorCount[PMT_502] = %d, want 3 (root-cause counted)", got)
	}
	if got := summary.PrimaryErrorCount["CHK_DOWNSTREAM"]; got != 0 {
		t.Errorf("PrimaryErrorCount[CHK_DOWNSTREAM] = %d, want 0 (propagation, not root cause)", got)
	}
	if got := summary.PrimaryErrorCount["GW_DOWNSTREAM"]; got != 0 {
		t.Errorf("PrimaryErrorCount[GW_DOWNSTREAM] = %d, want 0 (propagation, not root cause)", got)
	}
	if len(summary.PrimaryErrorCount) != 1 {
		t.Errorf("PrimaryErrorCount has %d entries, want 1 (only the root-cause code)", len(summary.PrimaryErrorCount))
	}

	// Every touched service (regardless of whether its name or hashed node
	// ID winds up in the facts due to upstream builder stubbing) participates
	// in every failed request exactly once.
	if len(summary.ServiceFailureCount) != 3 {
		t.Errorf("ServiceFailureCount has %d entries, want 3 (payment + checkout + api-gateway)", len(summary.ServiceFailureCount))
	}
	for svc, count := range summary.ServiceFailureCount {
		if count != 3 {
			t.Errorf("ServiceFailureCount[%s] = %d, want 3 (once per failed request)", svc, count)
		}
	}
}

// TestRollupWindow_EmptyStore covers the no-data path.
func TestRollupWindow_EmptyStore(t *testing.T) {
	s := store.NewStore()
	ts := tracestore.NewStore()
	now := time.Now().UTC()

	summary := RollupWindow(graphOf(s), s, ts, now.Add(-time.Minute), now)
	if summary.TotalRequests != 0 || summary.TotalFailures != 0 {
		t.Errorf("empty store rollup: total=%d failures=%d, want 0/0", summary.TotalRequests, summary.TotalFailures)
	}
	if len(summary.PrimaryErrorCount) != 0 {
		t.Errorf("empty store: PrimaryErrorCount has %d entries, want 0", len(summary.PrimaryErrorCount))
	}
}

// TestRollupWindow_NilStoreReturnsEmpty verifies the defensive nil-store path.
func TestRollupWindow_NilStoreReturnsEmpty(t *testing.T) {
	now := time.Now().UTC()
	summary := RollupWindow(nil, nil, nil, now.Add(-time.Minute), now)
	if summary.TotalRequests != 0 {
		t.Errorf("nil store: TotalRequests = %d, want 0", summary.TotalRequests)
	}
	if summary.PrimaryErrorCount == nil {
		t.Error("PrimaryErrorCount should be non-nil even with nil store")
	}
}

// TestRollupWindow_SuccessAndFailureMix verifies TotalRequests counts both
// and the successful request does not contribute to PrimaryErrorCount.
func TestRollupWindow_SuccessAndFailureMix(t *testing.T) {
	s := store.NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	now := time.Now().UTC()

	// One successful request
	okEv := testutil.MakeEvent(
		testutil.WithTraceID("aaaa00000000000000000000000000000"[:32]),
		testutil.WithService("api-gateway"),
		testutil.WithTimestamp(now.Add(-20*time.Second)),
	)
	r := b.BuildResult(okEv)
	s.Merge(r.Graph)

	// One failing cascade
	ingestCascade(t, s, ts, b, 42, now.Add(-15*time.Second))

	summary := RollupWindow(graphOf(s), s, ts, now.Add(-time.Minute), now)

	if summary.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalFailures != 1 {
		t.Errorf("TotalFailures = %d, want 1", summary.TotalFailures)
	}
	if summary.PrimaryErrorCount["PMT_502"] != 1 {
		t.Errorf("PrimaryErrorCount[PMT_502] = %d, want 1", summary.PrimaryErrorCount["PMT_502"])
	}
}

// graphOf is a test helper that reaches into the store's graph via its
// exposed accessor. Kept here so test intent stays local.
func graphOf(s *store.Store) *core.Graph {
	return s.Graph()
}
