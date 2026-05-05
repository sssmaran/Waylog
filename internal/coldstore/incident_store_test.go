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
