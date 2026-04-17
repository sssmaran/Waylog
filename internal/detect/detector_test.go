package detect

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

func TestDetector_NoSpike(t *testing.T) {
	s := store.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// 2 errors in current window — below MinCount of 3
	for i := range 2 {
		ingest(t, s, builder, testutil.MakeEvent(
			testutil.WithTraceID(fmt.Sprintf("aaaa%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("bbbb%012d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "fail"),
			testutil.WithTimestamp(now.Add(-30*time.Second)),
		))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, nil, nil)

	d.tick(nil)

	if d.Current() != nil {
		t.Fatal("expected no insight when count below threshold")
	}
}

func TestDetector_SpikeDetected(t *testing.T) {
	s := store.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// Baseline: 1 error in the baseline window
	ingest(t, s, builder, testutil.MakeEvent(
		testutil.WithTraceID("bbbb0000000000000000000000000000"),
		testutil.WithSpanID("cccc000000000000"),
		testutil.WithService("payment"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "fail"),
		testutil.WithUser("user-1", "standard", "us-east-1"),
		testutil.WithTimestamp(now.Add(-3*time.Minute)),
	))

	// Current: 5 errors in the current window — 5x lift, count >= 3
	for i := range 5 {
		ingest(t, s, builder, testutil.MakeEvent(
			testutil.WithTraceID(fmt.Sprintf("aaaa%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("dddd%012d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "fail"),
			testutil.WithUser(fmt.Sprintf("user-%d", i+10), "standard", "us-east-1"),
			testutil.WithTimestamp(now.Add(-20*time.Second)),
		))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, nil, nil)

	d.tick(nil)

	insight := d.Current()
	if insight == nil {
		t.Fatal("expected insight to be set")
	}
	if insight.TopErrorCode != "PMT_502" {
		t.Fatalf("TopErrorCode = %q, want PMT_502", insight.TopErrorCode)
	}
	if insight.CurrentCount != 5 {
		t.Fatalf("CurrentCount = %d, want 5", insight.CurrentCount)
	}
	if insight.Lift < 3.0 {
		t.Fatalf("Lift = %f, want >= 3.0", insight.Lift)
	}
	if insight.AffectedRequests != 5 {
		t.Fatalf("AffectedRequests = %d, want 5", insight.AffectedRequests)
	}
	if insight.AffectedUsers != 5 {
		t.Fatalf("AffectedUsers = %d, want 5", insight.AffectedUsers)
	}
}

func TestDetector_NewErrorCode(t *testing.T) {
	s := store.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// No baseline errors at all. 4 new errors in current window.
	for i := range 4 {
		ingest(t, s, builder, testutil.MakeEvent(
			testutil.WithTraceID(fmt.Sprintf("aaaa%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("dddd%012d", i)),
			testutil.WithService("db"),
			testutil.WithStatusCode(503),
			testutil.WithError("DB_503", "unavailable"),
			testutil.WithTimestamp(now.Add(-15*time.Second)),
		))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, nil, nil)

	d.tick(nil)

	insight := d.Current()
	if insight == nil {
		t.Fatal("expected insight for new error code")
	}
	if insight.TopErrorCode != "DB_503" {
		t.Fatalf("TopErrorCode = %q, want DB_503", insight.TopErrorCode)
	}
	if insight.BaselineCount != 0 {
		t.Fatalf("BaselineCount = %d, want 0", insight.BaselineCount)
	}
}

func TestDetector_AutoResolve(t *testing.T) {
	s := store.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// Spike: 5 errors in current window, 0 in baseline
	for i := range 5 {
		ingest(t, s, builder, testutil.MakeEvent(
			testutil.WithTraceID(fmt.Sprintf("aaaa%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("dddd%012d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "fail"),
			testutil.WithTimestamp(now.Add(-20*time.Second)),
		))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, nil, nil)

	// First tick detects the spike.
	d.tick(nil)
	if d.Current() == nil {
		t.Fatal("expected insight on first tick")
	}

	// Simulate time passing: move all errors into the baseline window
	// by adding them again with old timestamps.
	// Instead, just add enough baseline errors to drop the lift below threshold.
	for i := range 5 {
		ingest(t, s, builder, testutil.MakeEvent(
			testutil.WithTraceID(fmt.Sprintf("bbbb%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("eeee%012d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "fail"),
			testutil.WithTimestamp(now.Add(-3*time.Minute)),
		))
	}

	// Second tick: current=5, baseline=5, lift=1.0 — below threshold.
	d.tick(nil)
	if d.Current() != nil {
		t.Fatal("expected insight to auto-resolve when lift drops")
	}
}

func TestDetector_VIPTracking(t *testing.T) {
	s := store.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// 3 errors, 1 from a VIP user
	for i := range 3 {
		opts := []testutil.EventOption{
			testutil.WithTraceID(fmt.Sprintf("aaaa%028d", i)),
			testutil.WithSpanID(fmt.Sprintf("dddd%012d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "fail"),
			testutil.WithUser(fmt.Sprintf("user-%d", i), "standard", "us-east-1"),
			testutil.WithTimestamp(now.Add(-20 * time.Second)),
		}
		if i == 0 {
			opts = append(opts, testutil.WithVIP(true))
		}
		ingest(t, s, builder, testutil.MakeEvent(opts...))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, nil, nil)

	d.tick(nil)

	insight := d.Current()
	if insight == nil {
		t.Fatal("expected insight")
	}
	if insight.VIPUsers != 1 {
		t.Fatalf("VIPUsers = %d, want 1", insight.VIPUsers)
	}
}

func TestDetector_CascadingFailure_PicksRootCause(t *testing.T) {
	s := store.NewStore()
	ts := tracestore.NewStore()
	builder := build.NewBuilder()
	now := time.Now().UTC()

	// Simulate 3 cascading failures: payment → checkout → api-gateway.
	// Each request generates 3 error codes with the same count.
	// The detector should pick PMT_502 (leaf service) as root cause.
	for i := range 3 {
		traceID := fmt.Sprintf("cccc%028d", i)
		reqID := core.ID("request", traceID)
		// Payment span (leaf — no downstream, called by checkout)
		ingestWithTraces(t, s, ts, builder, traceID, reqID, testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID(fmt.Sprintf("p%015d", i)),
			testutil.WithParentSpanID(fmt.Sprintf("c%015d", i)),
			testutil.WithService("payment"),
			testutil.WithStatusCode(502),
			testutil.WithError("PMT_502", "payment failed"),
			testutil.WithCallerService("checkout"),
			testutil.WithTimestamp(now.Add(-20*time.Second)),
		))
		// Checkout span (middle — called by api-gateway, calls payment)
		ingestWithTraces(t, s, ts, builder, traceID, reqID, testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID(fmt.Sprintf("c%015d", i)),
			testutil.WithParentSpanID(fmt.Sprintf("a%015d", i)),
			testutil.WithService("checkout"),
			testutil.WithStatusCode(502),
			testutil.WithError("CHK_DOWNSTREAM", "downstream failed"),
			testutil.WithCallerService("api-gateway"),
			testutil.WithTimestamp(now.Add(-20*time.Second)),
		))
		// Api-gateway span (root — no caller)
		ingestWithTraces(t, s, ts, builder, traceID, reqID, testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithSpanID(fmt.Sprintf("a%015d", i)),
			testutil.WithParentSpanID(""),
			testutil.WithService("api-gateway"),
			testutil.WithStatusCode(502),
			testutil.WithError("GW_DOWNSTREAM", "downstream failed"),
			testutil.WithTimestamp(now.Add(-20*time.Second)),
		))
	}

	d := NewDetector(Config{
		Enabled:        true,
		Interval:       10 * time.Second,
		CurrentWindow:  1 * time.Minute,
		BaselineWindow: 5 * time.Minute,
		MinLift:        3.0,
		MinCount:       3,
	}, s, ts, nil)

	d.tick(nil)

	insight := d.Current()
	if insight == nil {
		t.Fatal("expected insight")
	}
	if insight.TopErrorCode != "PMT_502" {
		t.Fatalf("TopErrorCode = %q, want PMT_502 (root cause, not propagation error)", insight.TopErrorCode)
	}
}

func ingest(t *testing.T, s *store.Store, builder *build.Builder, ev event.WideEvent) {
	t.Helper()
	result := builder.BuildResult(ev)
	s.Merge(result.Graph)
}

func ingestWithTraces(t *testing.T, s *store.Store, ts *tracestore.Store, builder *build.Builder, traceID, reqID string, ev event.WideEvent) {
	t.Helper()
	result := builder.BuildResult(ev)
	s.Merge(result.Graph)
	if ts != nil && result.Span != nil {
		ts.Upsert(traceID, reqID, result.Span)
	}
}
