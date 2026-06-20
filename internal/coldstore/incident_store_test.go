package coldstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func TestIncidentStoreRoundtripAndPrune(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	users := 3
	inc := incidents.Incident{
		IncidentID:       incidents.StableID("prod", apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"}, now),
		Env:              "prod",
		Service:          "checkout",
		ErrorFamily:      apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"},
		Status:           incidents.StatusActive,
		Cause:            incidents.CauseDependency,
		Confidence:       incidents.ConfidenceHigh,
		Severity:         8,
		StartedAt:        now,
		UpdatedAt:        now,
		LastSeenAt:       now,
		AffectedRequests: 9,
		AffectedUsers:    &users,
		AffectedServices: 2,
		TopServices:      []string{"checkout", "payment"},
		SampleTraces:     []string{"trace-a", "trace-b"},
		Evidence:         []incidents.Evidence{{Kind: incidents.EvidenceTrace, Title: "trace", TraceID: "trace-a", OccurredAt: now}},
		NextChecks:       []string{"check downstream"},
		Lift:             9,
		CurrentCount:     9,
	}
	if err := store.Upsert(context.Background(), inc); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), inc.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IncidentID != inc.IncidentID || got.AffectedUsers == nil || *got.AffectedUsers != users || len(got.SampleTraces) != 2 {
		t.Fatalf("roundtrip=%+v", got)
	}
	active, err := store.ListActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active=%+v", active)
	}
	resolvedAt := now.Add(time.Minute)
	inc.Status = incidents.StatusResolved
	inc.ResolvedAt = &resolvedAt
	if err := store.Upsert(context.Background(), inc); err != nil {
		t.Fatal(err)
	}
	active, err = store.ListActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active after resolve=%+v", active)
	}
	deleted, err := store.PruneResolvedOlderThan(context.Background(), resolvedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d", deleted)
	}
	_, err = store.Get(context.Background(), inc.IncidentID)
	if !errors.Is(err, incidents.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestIncidentSuspectDeployRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	inc := incidents.Incident{
		IncidentID:      "inc_sd",
		Env:             "prod",
		Service:         "checkout",
		ErrorFamily:     apiv2.ErrorFamily{Service: "checkout", Step: "charge", ErrorCode: "X"},
		Status:          incidents.StatusActive,
		StartedAt:       now,
		UpdatedAt:       now,
		LastSeenAt:      now,
		SuspectDeployID: "dep_42",
	}
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "inc_sd")
	if err != nil {
		t.Fatal(err)
	}
	if got.SuspectDeployID != "dep_42" {
		t.Fatalf("round-trip SuspectDeployID = %q, want dep_42", got.SuspectDeployID)
	}

	// A later upsert with an empty id must not clobber the stored correlation.
	inc.SuspectDeployID = ""
	inc.UpdatedAt = now.Add(time.Minute)
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, "inc_sd")
	if err != nil {
		t.Fatal(err)
	}
	if got.SuspectDeployID != "dep_42" {
		t.Fatalf("empty upsert clobbered sticky id: %q", got.SuspectDeployID)
	}
}

func TestPruneResolvedNeverTouchesActiveOrRecovering(t *testing.T) {
	ctx := context.Background()
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	old := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	for _, inc := range []incidents.Incident{
		testColdIncident("inc_active", incidents.StatusActive, old),
		testColdIncident("inc_recovering", incidents.StatusRecovering, old),
		testColdIncident("inc_resolved", incidents.StatusResolved, old),
	} {
		if err := store.Upsert(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}

	// Cutoff far in the future: everything resolved is eligible.
	deleted, err := store.PruneResolvedOlderThan(ctx, old.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want only the resolved row", deleted)
	}
	for _, id := range []string{"inc_active", "inc_recovering"} {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("%s must survive pruning: %v", id, err)
		}
	}
	if _, err := store.Get(ctx, "inc_resolved"); !errors.Is(err, incidents.ErrNotFound) {
		t.Fatalf("resolved row must be pruned, got %v", err)
	}
}

func TestIncidentStoreReplaceNonResolved(t *testing.T) {
	ctx := context.Background()
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	oldActive := testColdIncident("inc_old_active", incidents.StatusActive, now.Add(-20*time.Minute))
	oldRecovering := testColdIncident("inc_old_recovering", incidents.StatusRecovering, now.Add(-15*time.Minute))
	preservedResolved := testColdIncident("inc_preserved_resolved", incidents.StatusResolved, now.Add(-30*time.Minute))
	overwrittenResolved := testColdIncident("inc_overwritten_resolved", incidents.StatusResolved, now.Add(-25*time.Minute))
	for _, inc := range []incidents.Incident{oldActive, oldRecovering, preservedResolved, overwrittenResolved} {
		if err := store.Upsert(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}

	newActive := testColdIncident("inc_new_active", incidents.StatusActive, now)
	replacement := testColdIncident("inc_overwritten_resolved", incidents.StatusActive, now)
	if err := store.ReplaceNonResolved(ctx, []incidents.Incident{newActive, replacement}); err != nil {
		t.Fatal(err)
	}

	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotActive := map[string]incidents.Status{}
	for _, inc := range active {
		gotActive[inc.IncidentID] = inc.Status
	}
	if _, ok := gotActive["inc_old_active"]; ok {
		t.Fatalf("old active row preserved unexpectedly: %+v", gotActive)
	}
	if _, ok := gotActive["inc_old_recovering"]; ok {
		t.Fatalf("old recovering row preserved unexpectedly: %+v", gotActive)
	}
	if gotActive["inc_new_active"] != incidents.StatusActive || gotActive["inc_overwritten_resolved"] != incidents.StatusActive {
		t.Fatalf("active rows after replace=%+v", gotActive)
	}
	if got, err := store.Get(ctx, "inc_preserved_resolved"); err != nil || got.Status != incidents.StatusResolved {
		t.Fatalf("preserved resolved row got=%+v err=%v", got, err)
	}
}

func TestIncidentStore_PropagationSnapshotRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	inc := baseEvidenceIncident("inc_test_prop", ts)
	inc.Propagation = &incidents.PropagationSnapshot{
		Opening: &incidents.PropagationEvidence{
			OriginService: "payment-service",
			OriginStep:    "charge",
			Path: []incidents.PropagationStep{
				{Service: "payment-service", Step: "charge", Status: "error", ErrorCode: "DB_TIMEOUT"},
			},
			SampleTraceID: "trace_a",
			CapturedAt:    ts,
			CaptureStatus: incidents.CaptureOK,
		},
		Latest: &incidents.PropagationEvidence{
			OriginService: "payment-service",
			OriginStep:    "charge",
			Path: []incidents.PropagationStep{
				{Service: "payment-service", Step: "charge", Status: "error", ErrorCode: "DB_TIMEOUT"},
			},
			SampleTraceID: "trace_b",
			CapturedAt:    ts.Add(30 * time.Second),
			CaptureStatus: incidents.CaptureOK,
		},
	}
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Propagation == nil {
		t.Fatal("Propagation lost")
	}
	if got.Propagation.Opening == nil || got.Propagation.Latest == nil {
		t.Fatal("Opening/Latest lost")
	}
	if got.Propagation.Opening.SampleTraceID != "trace_a" {
		t.Errorf("Opening.SampleTraceID = %q", got.Propagation.Opening.SampleTraceID)
	}
	if got.Propagation.Latest.SampleTraceID != "trace_b" {
		t.Errorf("Latest.SampleTraceID = %q", got.Propagation.Latest.SampleTraceID)
	}
	if got.Propagation.Opening.CaptureStatus != incidents.CaptureOK {
		t.Errorf("Opening.CaptureStatus = %q", got.Propagation.Opening.CaptureStatus)
	}
}

func TestIncidentStore_BlastSnapshotRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	openUsers := 12
	latestUsers := 47
	inc := baseEvidenceIncident("inc_test_blast", ts)
	inc.Blast = &incidents.BlastSnapshot{
		Opening: &incidents.BlastEvidence{
			AffectedRequests: 5,
			AffectedUsers:    &openUsers,
			AffectedServices: 1,
			TopServices:      []string{"checkout"},
			SampledTraces:    []string{"trace_a"},
			CapturedAt:       ts,
			CaptureStatus:    incidents.CaptureOK,
		},
		Latest: &incidents.BlastEvidence{
			AffectedRequests: 184,
			AffectedUsers:    &latestUsers,
			AffectedServices: 3,
			TopServices:      []string{"checkout", "api-gateway"},
			SampledTraces:    []string{"trace_b", "trace_c"},
			CapturedAt:       ts.Add(time.Minute),
			CaptureStatus:    incidents.CaptureOK,
		},
	}
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Blast == nil || got.Blast.Opening == nil || got.Blast.Latest == nil {
		t.Fatalf("Blast snapshot lost: %+v", got.Blast)
	}
	if got.Blast.Opening.AffectedRequests != 5 || got.Blast.Latest.AffectedRequests != 184 {
		t.Errorf("AffectedRequests round-trip wrong: opening=%d latest=%d",
			got.Blast.Opening.AffectedRequests, got.Blast.Latest.AffectedRequests)
	}
	if got.Blast.Opening.AffectedServices != 1 || got.Blast.Latest.AffectedServices != 3 {
		t.Errorf("AffectedServices round-trip wrong")
	}
	if got.Blast.Opening.AffectedUsers == nil || *got.Blast.Opening.AffectedUsers != openUsers {
		t.Errorf("Opening.AffectedUsers lost: %+v", got.Blast.Opening.AffectedUsers)
	}
	if got.Blast.Latest.AffectedUsers == nil || *got.Blast.Latest.AffectedUsers != latestUsers {
		t.Errorf("Latest.AffectedUsers lost: %+v", got.Blast.Latest.AffectedUsers)
	}
}

func TestIncidentStore_RuntimeSnapshotRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	inc := baseEvidenceIncident("inc_test_runtime", ts)
	oom := incidents.RuntimeEvidence{
		SignalID: "sig_oom", Subtype: "oom_killed", Service: "checkout", Source: "k8s-demo",
		Severity: "critical", Reason: "OOMKilled", OccurredAt: ts.Add(-2 * time.Minute), CapturedAt: ts,
		CaptureStatus: incidents.CaptureOK, Metadata: map[string]any{"pod": "checkout-7f8b9c-x2k"},
	}
	panicEv := incidents.RuntimeEvidence{
		SignalID: "sig_panic", Subtype: "panic", Service: "checkout", Source: "go-sdk",
		Severity: "warning", Reason: "runtime panic", OccurredAt: ts.Add(-time.Minute), CapturedAt: ts,
		CaptureStatus: incidents.CaptureOK,
	}
	inc.Runtime = &incidents.RuntimeSnapshot{Matches: []incidents.RuntimeEvidence{oom, panicEv}, Opening: &oom, Latest: &panicEv}
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Runtime == nil || len(got.Runtime.Matches) != 2 {
		t.Fatalf("Runtime snapshot lost: %+v", got.Runtime)
	}
	if got.Runtime.Matches[0].Subtype != "oom_killed" || got.Runtime.Matches[1].Subtype != "panic" {
		t.Errorf("runtime subtypes round-trip wrong: %+v", got.Runtime.Matches)
	}
	if got.Runtime.Opening == nil || got.Runtime.Opening.SignalID != "sig_oom" {
		t.Errorf("Opening lost: %+v", got.Runtime.Opening)
	}
	if got.Runtime.Latest == nil || got.Runtime.Latest.SignalID != "sig_panic" {
		t.Errorf("Latest lost: %+v", got.Runtime.Latest)
	}
	if got.Runtime.Matches[0].Metadata["pod"] != "checkout-7f8b9c-x2k" {
		t.Errorf("metadata lost: %+v", got.Runtime.Matches[0].Metadata)
	}
}

func TestIncidentStore_NilSnapshotsRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	inc := baseEvidenceIncident("inc_test_nil", ts)
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Propagation != nil {
		t.Errorf("Propagation = %+v; want nil", got.Propagation)
	}
	if got.Blast != nil {
		t.Errorf("Blast = %+v; want nil", got.Blast)
	}
	if got.Alerts != nil {
		t.Errorf("Alerts = %+v; want nil", got.Alerts)
	}
}

func TestIncidentStore_AlertSnapshotRoundTrip(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	inc := baseEvidenceIncident("inc_test_alerts", ts)
	inc.Alerts = &incidents.AlertSnapshot{
		Opening: &incidents.AlertEvidence{
			Matches: []incidents.MatchedAlert{{
				SignalID:    "sig_open",
				AlertID:     "CheckoutPaymentFailure",
				Source:      "alertmanager",
				Severity:    "critical",
				Reason:      "PMT_502 spike",
				ProviderURL: "https://alerts.example/open",
				EvidenceIDs: []string{"sig_open"},
				MatchedAt:   ts,
				Strategy:    "family",
			}},
			CapturedAt:    ts,
			CaptureStatus: incidents.CaptureOK,
		},
		Latest: &incidents.AlertEvidence{
			Matches: []incidents.MatchedAlert{{
				SignalID:    "sig_latest",
				AlertID:     "CheckoutPaymentFailure",
				Source:      "alertmanager",
				Severity:    "critical",
				Reason:      "PMT_502 still firing",
				EvidenceIDs: []string{"sig_latest"},
				MatchedAt:   ts.Add(time.Minute),
				Strategy:    "family",
			}},
			CapturedAt:    ts.Add(time.Minute),
			CaptureStatus: incidents.CaptureOK,
		},
	}
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, inc.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Alerts == nil || got.Alerts.Opening == nil || got.Alerts.Latest == nil {
		t.Fatalf("Alerts snapshot lost: %+v", got.Alerts)
	}
	if got.Alerts.Opening.Matches[0].SignalID != "sig_open" {
		t.Fatalf("opening match = %+v", got.Alerts.Opening.Matches)
	}
	if got.Alerts.Latest.Matches[0].SignalID != "sig_latest" {
		t.Fatalf("latest match = %+v", got.Alerts.Latest.Matches)
	}
	if got.Alerts.Latest.CaptureStatus != incidents.CaptureOK {
		t.Fatalf("latest status = %s", got.Alerts.Latest.CaptureStatus)
	}
}

func TestIncidentStore_DoesNotMergeOpening(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := NewIncidentStore(managed.(*SQLiteStore))
	ctx := context.Background()
	ts := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := baseEvidenceIncident("inc_test_dumb", ts)
	base.Propagation = &incidents.PropagationSnapshot{
		Opening: &incidents.PropagationEvidence{SampleTraceID: "trace_a", CapturedAt: ts, CaptureStatus: incidents.CaptureOK},
		Latest:  &incidents.PropagationEvidence{SampleTraceID: "trace_a", CapturedAt: ts, CaptureStatus: incidents.CaptureOK},
	}
	if err := store.Upsert(ctx, base); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	base.Propagation = &incidents.PropagationSnapshot{
		Latest: &incidents.PropagationEvidence{SampleTraceID: "trace_b", CapturedAt: ts.Add(time.Minute), CaptureStatus: incidents.CaptureOK},
	}
	if err := store.Upsert(ctx, base); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, err := store.Get(ctx, base.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Propagation == nil || got.Propagation.Opening != nil {
		t.Errorf("Opening should be nil after explicit overwrite; got %+v", got.Propagation)
	}
	if got.Propagation == nil || got.Propagation.Latest == nil || got.Propagation.Latest.SampleTraceID != "trace_b" {
		t.Errorf("Latest should be trace_b; got %+v", got.Propagation)
	}
}

func testColdIncident(id string, status incidents.Status, at time.Time) incidents.Incident {
	resolvedAt := at.Add(time.Minute)
	inc := incidents.Incident{
		IncidentID:       id,
		Env:              "prod",
		Service:          "checkout",
		ErrorFamily:      apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"},
		Status:           status,
		Cause:            incidents.CauseDependency,
		Confidence:       incidents.ConfidenceHigh,
		Severity:         8,
		StartedAt:        at,
		UpdatedAt:        at,
		LastSeenAt:       at,
		AffectedRequests: 9,
		AffectedServices: 2,
		TopServices:      []string{"checkout", "payment"},
		SampleTraces:     []string{"trace-a"},
		Evidence:         []incidents.Evidence{{Kind: incidents.EvidenceTrace, Title: "trace", TraceID: "trace-a", OccurredAt: at}},
		NextChecks:       []string{"check downstream"},
		Lift:             9,
		CurrentCount:     9,
	}
	if status == incidents.StatusResolved {
		inc.ResolvedAt = &resolvedAt
	}
	return inc
}

// baseEvidenceIncident returns a minimal active incident shared across the
// Propagation/Blast snapshot round-trip tests. Tests set the relevant snapshot
// field after construction.
func baseEvidenceIncident(id string, ts time.Time) incidents.Incident {
	return incidents.Incident{
		IncidentID:  id,
		Env:         "demo",
		Service:     "payment-service",
		ErrorFamily: apiv2.ErrorFamily{Service: "payment-service", Step: "charge", ErrorCode: "DB_TIMEOUT"},
		Status:      incidents.StatusActive,
		Cause:       incidents.CauseUnknown,
		Confidence:  incidents.ConfidenceMedium,
		Severity:    2,
		StartedAt:   ts,
		UpdatedAt:   ts,
		LastSeenAt:  ts,
	}
}
