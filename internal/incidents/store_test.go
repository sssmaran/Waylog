package incidents

import (
	"context"
	"testing"
	"time"
)

// TestStaleActiveTransitionTransitionsOnlyStaleRows mirrors the rebuild-time
// policy implemented in cmd/ingest/main.go: when the WAL replay returns zero
// events but the seed has active rows older than the replay-since cutoff,
// only those stale rows transition to recovering. Non-stale active rows in
// the same seed are left untouched.
func TestStaleActiveTransitionTransitionsOnlyStaleRows(t *testing.T) {
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	replaySince := now.Add(-1 * time.Hour)
	store := NewMemoryStore()

	stale := testIncident(now.Add(-2 * time.Hour)) // older than replaySince
	stale.IncidentID = "inc_stale"
	fresh := testIncident(now.Add(-15 * time.Minute)) // within replaySince
	fresh.IncidentID = "inc_fresh"

	if err := store.Upsert(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}

	// Policy: only mutate rows whose StartedAt precedes replaySince and whose
	// status is active. Replicate the same predicate the rebuild path uses.
	active, _ := store.ListActive(context.Background())
	transitioned := 0
	for _, inc := range active {
		if inc.Status != StatusActive {
			continue
		}
		if !inc.StartedAt.Before(replaySince) {
			continue
		}
		inc.Status = StatusRecovering
		recoveryTS := now
		inc.RecoveringAt = &recoveryTS
		inc.UpdatedAt = now
		if err := store.Upsert(context.Background(), inc); err != nil {
			t.Fatalf("upsert stale row: %v", err)
		}
		transitioned++
	}
	if transitioned != 1 {
		t.Fatalf("transitioned = %d, want 1 (only stale row)", transitioned)
	}

	gotStale, _ := store.Get(context.Background(), "inc_stale")
	if gotStale.Status != StatusRecovering {
		t.Fatalf("stale row status = %q, want recovering", gotStale.Status)
	}
	if gotStale.RecoveringAt == nil {
		t.Fatal("recovering_at not set on stale row")
	}
	gotFresh, _ := store.Get(context.Background(), "inc_fresh")
	if gotFresh.Status != StatusActive {
		t.Fatalf("fresh row status = %q, want active (must not be mutated)", gotFresh.Status)
	}
	if gotFresh.RecoveringAt != nil {
		t.Fatal("fresh row recovering_at must be unset")
	}
}
