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
