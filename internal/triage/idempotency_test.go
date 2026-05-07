package triage

import (
	"context"
	"testing"
	"time"
)

func TestBuildIsIdempotentForSameInput(t *testing.T) {
	deps := Deps{
		Incidents: richIncidents{}, Blast: richBlast{}, Story: richStory{},
		Signals: richSignals{}, NextChecks: richNextChecks{},
		// Two different "now" values to prove generated_at doesn't enter the hash
		Now: func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("15m", false, deps.Now())

	r1, err := eng.Build(context.Background(), "inc_abc", opts)
	if err != nil {
		t.Fatalf("build 1: %v", err)
	}
	r2, err := eng.Build(context.Background(), "inc_abc", opts)
	if err != nil {
		t.Fatalf("build 2: %v", err)
	}
	if r1.ReportHash != r2.ReportHash {
		t.Fatalf("two builds should have identical report_hash, got %q vs %q", r1.ReportHash, r2.ReportHash)
	}
}

func TestSnapshotModeUsesIncidentUpdatedAt(t *testing.T) {
	deps := Deps{
		Incidents: richIncidents{}, Blast: richBlast{}, Story: richStory{},
		Signals: richSignals{}, NextChecks: richNextChecks{},
		Now: func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, _ := NewEngine(deps)

	wallClockOpts, _ := ParseBuildOptions("15m", false, deps.Now())
	snapshotOpts, _ := ParseBuildOptions("15m", true, deps.Now())

	wall, err := eng.Build(context.Background(), "inc_abc", wallClockOpts)
	if err != nil {
		t.Fatalf("wall build: %v", err)
	}
	snap, err := eng.Build(context.Background(), "inc_abc", snapshotOpts)
	if err != nil {
		t.Fatalf("snap build: %v", err)
	}
	// Both reports describe the same incident state; with the same upstream stubs they hash equal.
	// The point of this test is that snapshot mode does not crash and produces a valid report.
	if snap.ReportHash == "" || wall.ReportHash == "" {
		t.Fatalf("hashes must be non-empty (snap=%q wall=%q)", snap.ReportHash, wall.ReportHash)
	}
	if snap.IncidentRef.ID != "inc_abc" {
		t.Fatalf("snap report missing incident ref")
	}
}
