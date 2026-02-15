package tracestory

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func buildThreeHopGraph(t *testing.T, traceID string, paymentFail bool) *store.Store {
	t.Helper()
	s := store.NewStore()
	b := build.NewBuilder()

	// Hop 1: api-gateway (root)
	gwEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithUser("user-42", "premium", "us-west-2"),
		testutil.WithFlow("checkout"),
		testutil.WithFeatureFlags("dark-mode"),
		testutil.WithCallerService(""),
	)
	s.Merge(b.Build(gwEv))

	// Hop 2: checkout-demo (child of gateway)
	ckEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithParentSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithService("checkout-demo"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(32),
		testutil.WithUser("user-42", "premium", "us-west-2"),
		testutil.WithCallerService("api-gateway"),
	)
	s.Merge(b.Build(ckEv))

	// Hop 3: payment-demo (child of checkout)
	pmOpts := []testutil.EventOption{
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithService("payment-demo"),
		testutil.WithLatency(12),
		testutil.WithUser("user-42", "premium", "us-west-2"),
		testutil.WithCallerService("checkout-demo"),
	}
	if paymentFail {
		pmOpts = append(pmOpts,
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "payment failed"),
		)
	} else {
		pmOpts = append(pmOpts, testutil.WithStatusCode(200))
	}
	s.Merge(b.Build(testutil.MakeEvent(pmOpts...)))

	return s
}

func TestBuild_SuccessChain(t *testing.T) {
	traceID := "11111111111111111111111111111111"
	s := buildThreeHopGraph(t, traceID, false)

	story, ctx, err := Build(s.Snapshot(), traceID)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if story.TraceID != traceID {
		t.Errorf("TraceID = %s, want %s", story.TraceID, traceID)
	}
	if story.HopCount != 3 {
		t.Errorf("HopCount = %d, want 3", story.HopCount)
	}
	if !story.Success {
		t.Error("Success should be true for all-200 trace")
	}
	if story.FirstFailHop != nil {
		t.Error("FirstFailHop should be nil for success trace")
	}

	// Verify chain order: gateway → checkout → payment
	if len(story.Chain) != 3 {
		t.Fatalf("Chain length = %d, want 3", len(story.Chain))
	}
	expectedServices := []string{"api-gateway", "checkout-demo", "payment-demo"}
	for i, want := range expectedServices {
		if story.Chain[i].Service != want {
			t.Errorf("Chain[%d].Service = %s, want %s", i, story.Chain[i].Service, want)
		}
	}

	// Verify hop fields
	if story.Chain[0].StatusCode != 200 {
		t.Errorf("Chain[0].StatusCode = %d, want 200", story.Chain[0].StatusCode)
	}
	if story.Chain[0].LatencyMs != 45 {
		t.Errorf("Chain[0].LatencyMs = %d, want 45", story.Chain[0].LatencyMs)
	}
	if !story.Chain[0].IsRoot {
		t.Error("Chain[0].IsRoot should be true")
	}
	if story.Chain[1].IsRoot {
		t.Error("Chain[1].IsRoot should be false")
	}

	// Verify context
	if ctx.UserTier != "premium" {
		t.Errorf("UserTier = %s, want premium", ctx.UserTier)
	}
	if ctx.UserRegion != "us-west-2" {
		t.Errorf("UserRegion = %s, want us-west-2", ctx.UserRegion)
	}
	if ctx.UserID != "user-42" {
		t.Errorf("UserID = %s, want user-42", ctx.UserID)
	}
	if ctx.Flow != "checkout" {
		t.Errorf("Flow = %s, want checkout", ctx.Flow)
	}
}

func TestBuild_PaymentFail(t *testing.T) {
	traceID := "22222222222222222222222222222222"
	s := buildThreeHopGraph(t, traceID, true)

	story, _, err := Build(s.Snapshot(), traceID)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if story.Success {
		t.Error("Success should be false when payment fails")
	}
	if story.FirstFailHop == nil {
		t.Fatal("FirstFailHop should not be nil")
	}
	if story.FirstFailHop.Service != "payment-demo" {
		t.Errorf("FirstFailHop.Service = %s, want payment-demo", story.FirstFailHop.Service)
	}
	if story.FirstFailHop.ErrorCode != "PMT_502" {
		t.Errorf("FirstFailHop.ErrorCode = %s, want PMT_502", story.FirstFailHop.ErrorCode)
	}
	if story.FirstFailHop.StatusCode != 502 {
		t.Errorf("FirstFailHop.StatusCode = %d, want 502", story.FirstFailHop.StatusCode)
	}
}

func TestBuild_SingleHop(t *testing.T) {
	traceID := "33333333333333333333333333333333"
	s := store.NewStore()
	b := build.NewBuilder()

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(10),
	)
	s.Merge(b.Build(ev))

	story, _, err := Build(s.Snapshot(), traceID)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if story.HopCount != 1 {
		t.Errorf("HopCount = %d, want 1", story.HopCount)
	}
	if !story.Success {
		t.Error("Success should be true")
	}
	if story.Chain[0].Service != "api-gateway" {
		t.Errorf("Chain[0].Service = %s, want api-gateway", story.Chain[0].Service)
	}
}

func TestBuild_UnknownTrace(t *testing.T) {
	s := store.NewStore()
	_, _, err := Build(s.Snapshot(), "00000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for unknown trace")
	}
}

func TestBuild_Context(t *testing.T) {
	traceID := "44444444444444444444444444444444"
	s := buildThreeHopGraph(t, traceID, false)

	_, ctx, err := Build(s.Snapshot(), traceID)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if ctx.Flow != "checkout" {
		t.Errorf("Flow = %s, want checkout", ctx.Flow)
	}
	if ctx.UserTier != "premium" {
		t.Errorf("UserTier = %s, want premium", ctx.UserTier)
	}
	if ctx.UserRegion != "us-west-2" {
		t.Errorf("UserRegion = %s, want us-west-2", ctx.UserRegion)
	}
	if ctx.UserID != "user-42" {
		t.Errorf("UserID = %s, want user-42", ctx.UserID)
	}
	if len(ctx.Flags) == 0 {
		t.Error("expected at least one feature flag")
	} else if ctx.Flags[0] != "dark-mode" {
		t.Errorf("Flags[0] = %s, want dark-mode", ctx.Flags[0])
	}
}

func TestBuild_ToleratesSnapshotAttrTypes(t *testing.T) {
	traceID := "55555555555555555555555555555555"
	s := store.NewStore()
	b := build.NewBuilder()

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(12),
		testutil.WithUser("user-99", "standard", "us-east-1"),
	)
	s.Merge(b.Build(ev))

	snap := s.Snapshot()
	spanNodeID := core.ID("span", traceID, ev.Request.SpanID)
	span, ok := snap.Nodes[spanNodeID]
	if !ok {
		t.Fatalf("span node %s not found", spanNodeID)
	}

	// Simulate snapshot-decoded attribute types.
	span.Attr["status_code"] = float64(200)
	span.Attr["latency_ms"] = float64(12)
	span.Attr["success"] = "true"
	span.Attr["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	snap.Nodes[spanNodeID] = span

	story, _, err := Build(snap, traceID)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(story.Chain) != 1 {
		t.Fatalf("chain length = %d, want 1", len(story.Chain))
	}
	if story.Chain[0].StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", story.Chain[0].StatusCode)
	}
	if story.Chain[0].LatencyMs != 12 {
		t.Errorf("LatencyMs = %d, want 12", story.Chain[0].LatencyMs)
	}
	if !story.Chain[0].Success {
		t.Error("Success should parse from string true")
	}
	if story.Chain[0].Timestamp.IsZero() {
		t.Error("Timestamp should parse from RFC3339 string")
	}
}
