package analysis

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/store"
)

func TestDiffSummaries_RequestAndLatency(t *testing.T) {
	before := store.WindowSummary{
		TotalRequests: 100,
		TotalFailures: 10,
		LatencyP50:    50,
		LatencyP95:    200,
		LatencyP99:    500,
		ErrorCount:    map[string]int{"err1": 5},
	}
	after := store.WindowSummary{
		TotalRequests: 150,
		TotalFailures: 25,
		LatencyP50:    60,
		LatencyP95:    250,
		LatencyP99:    600,
		ErrorCount:    map[string]int{"err1": 10, "err2": 3},
	}

	diff := DiffSummaries(before, after)

	if diff.TotalRequestsBefore != 100 || diff.TotalRequestsAfter != 150 {
		t.Errorf("TotalRequests before/after = %d/%d, want 100/150",
			diff.TotalRequestsBefore, diff.TotalRequestsAfter)
	}
	if diff.TotalFailuresBefore != 10 || diff.TotalFailuresAfter != 25 {
		t.Errorf("TotalFailures before/after = %d/%d, want 10/25",
			diff.TotalFailuresBefore, diff.TotalFailuresAfter)
	}
	if diff.LatencyP50Before != 50 || diff.LatencyP50After != 60 {
		t.Errorf("LatencyP50 before/after = %d/%d, want 50/60",
			diff.LatencyP50Before, diff.LatencyP50After)
	}
	if diff.LatencyP95Before != 200 || diff.LatencyP95After != 250 {
		t.Errorf("LatencyP95 before/after = %d/%d, want 200/250",
			diff.LatencyP95Before, diff.LatencyP95After)
	}
	if diff.LatencyP99Before != 500 || diff.LatencyP99After != 600 {
		t.Errorf("LatencyP99 before/after = %d/%d, want 500/600",
			diff.LatencyP99Before, diff.LatencyP99After)
	}

	// err1 should be in Increased
	if len(diff.Increased) != 1 || diff.Increased[0].ErrorCode != "err1" {
		t.Errorf("Increased = %+v, want [{err1 5 10 5}]", diff.Increased)
	}
	// err2 should be in New
	if len(diff.New) != 1 || diff.New[0].ErrorCode != "err2" {
		t.Errorf("New = %+v, want [{err2 0 3 3}]", diff.New)
	}
}
