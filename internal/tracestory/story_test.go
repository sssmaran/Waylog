package tracestory

import (
	"slices"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func TestBuildWithTraceStore_SuccessChain(t *testing.T) {
	traceID := "11111111111111111111111111111111"
	graphStore, traceStore := buildThreeHopTrace(t, traceID, false)

	story, ctx, err := BuildWithTraceStore(graphStore.Snapshot(), traceStore, traceID)
	if err != nil {
		t.Fatalf("BuildWithTraceStore() error: %v", err)
	}
	if story.HopCount != 3 {
		t.Fatalf("HopCount = %d, want 3", story.HopCount)
	}
	if !story.Success || story.FirstFailHop != nil {
		t.Fatalf("unexpected story success state: %+v", story)
	}
	want := []string{"api-gateway", "checkout", "payment"}
	for i := range want {
		if story.Chain[i].Service != want[i] {
			t.Fatalf("Chain[%d].Service = %q, want %q", i, story.Chain[i].Service, want[i])
		}
	}
	if ctx.UserID != "user-42" || ctx.UserTier != "premium" || ctx.UserRegion != "us-west-2" {
		t.Fatalf("unexpected context: %+v", ctx)
	}
	if !slices.Contains(ctx.Flags, "dark-mode") {
		t.Fatalf("expected dark-mode in context flags, got %v", ctx.Flags)
	}
}

func TestBuildWithTraceStore_FailureChain(t *testing.T) {
	traceID := "22222222222222222222222222222222"
	graphStore, traceStore := buildThreeHopTrace(t, traceID, true)

	story, ctx, err := BuildWithTraceStore(graphStore.Snapshot(), traceStore, traceID)
	if err != nil {
		t.Fatalf("BuildWithTraceStore() error: %v", err)
	}
	if story.Success {
		t.Fatal("expected failed trace")
	}
	if story.FirstFailHop == nil {
		t.Fatal("expected first failing hop")
	}
	if story.FirstFailHop.Service != "payment" {
		t.Fatalf("FirstFailHop.Service = %q, want payment", story.FirstFailHop.Service)
	}
	if story.FirstFailHop.ErrorCode != "PMT_502" {
		t.Fatalf("FirstFailHop.ErrorCode = %q, want PMT_502", story.FirstFailHop.ErrorCode)
	}
	if !slices.Contains(ctx.ErrorCodes, "PMT_502") {
		t.Fatalf("expected PMT_502 in context error codes, got %v", ctx.ErrorCodes)
	}
}

func TestBuildWithTraceStore_SingleHop(t *testing.T) {
	traceID := "33333333333333333333333333333333"
	graphStore := store.NewStore()
	traceStore := tracestore.NewStore()

	builder := build.NewBuilder()

	ev := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(10),
	)
	upsertEvent(t, graphStore, traceStore, builder, ev)

	story, _, err := BuildWithTraceStore(graphStore.Snapshot(), traceStore, traceID)
	if err != nil {
		t.Fatalf("BuildWithTraceStore() error: %v", err)
	}
	if story.HopCount != 1 || story.Chain[0].Service != "api-gateway" {
		t.Fatalf("unexpected story: %+v", story)
	}
}

func TestBuildWithTraceStore_OrdersSiblingHopsByTimestamp(t *testing.T) {
	traceID := "66666666666666666666666666666666"
	graphStore := store.NewStore()
	traceStore := tracestore.NewStore()

	builder := build.NewBuilder()
	base := time.Now().UTC()

	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithParentSpanID(""),
			testutil.WithService("api-gateway"),
			testutil.WithTimestamp(base.Add(1*time.Millisecond)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithParentSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithService("checkout"),
			testutil.WithTimestamp(base.Add(2*time.Millisecond)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("dddddddddddddddd"),
			testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithService("payment"),
			testutil.WithTimestamp(base.Add(4*time.Millisecond)),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("cccccccccccccccc"),
			testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithService("db"),
			testutil.WithTimestamp(base.Add(3*time.Millisecond)),
		),
	}

	for _, ev := range events {
		upsertEvent(t, graphStore, traceStore, builder, ev)
	}

	story, _, err := BuildWithTraceStore(graphStore.Snapshot(), traceStore, traceID)
	if err != nil {
		t.Fatalf("BuildWithTraceStore() error: %v", err)
	}
	want := []string{"api-gateway", "checkout", "db", "payment"}
	if len(story.Chain) != len(want) {
		t.Fatalf("chain length = %d, want %d", len(story.Chain), len(want))
	}
	for i := range want {
		if story.Chain[i].Service != want[i] {
			t.Fatalf("Chain[%d].Service = %q, want %q", i, story.Chain[i].Service, want[i])
		}
	}
}

func TestBuild_UnknownTrace(t *testing.T) {
	graphStore := store.NewStore()
	_, _, err := Build(graphStore.Snapshot(), "00000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for unknown trace")
	}
}

func buildThreeHopTrace(t *testing.T, traceID string, paymentFail bool) (*store.Store, *tracestore.Store) {
	t.Helper()

	graphStore := store.NewStore()
	traceStore := tracestore.NewStore()

	builder := build.NewBuilder()

	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithParentSpanID(""),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(45),
			testutil.WithUser("user-42", "premium", "us-west-2"),
			testutil.WithFlow("checkout"),
			testutil.WithFeatureFlags("dark-mode"),
		),
		testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithParentSpanID("aaaaaaaaaaaaaaaa"),
			testutil.WithService("checkout"),
			testutil.WithStatusCode(200),
			testutil.WithLatency(32),
			testutil.WithUser("user-42", "premium", "us-west-2"),
			testutil.WithCallerService("api-gateway"),
		),
	}

	payment := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(12),
		testutil.WithUser("user-42", "premium", "us-west-2"),
		testutil.WithCallerService("checkout"),
	)
	if paymentFail {
		payment = testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("cccccccccccccccc"),
			testutil.WithParentSpanID("bbbbbbbbbbbbbbbb"),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithLatency(12),
			testutil.WithUser("user-42", "premium", "us-west-2"),
			testutil.WithCallerService("checkout"),
			testutil.WithError("PMT_502", "payment failed"),
		)
	}
	events = append(events, payment)

	for _, ev := range events {
		upsertEvent(t, graphStore, traceStore, builder, ev)
	}
	return graphStore, traceStore
}

func upsertEvent(t *testing.T, graphStore *store.Store, traceStore *tracestore.Store, builder *build.Builder, ev event.WideEvent) {
	t.Helper()
	result := builder.BuildResult(ev)
	graphStore.Merge(result.Graph)
	if result.Span != nil {
		traceStore.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), result.Span)
	}
}
