package analysis

import "github.com/sssmaran/WaylogCLI/internal/graph/store"

// DiffRollups is the canonical diff for default user-facing comparisons.
// It consumes root-cause-counted [RollupSummary]s so compare_windows /
// anomaly detection / overview deltas never re-introduce propagation
// amplification. DiffSummaries remains for detail surfaces that need
// propagation-counted spread.
func DiffRollups(before, after RollupSummary) WindowDiff {
	out := WindowDiff{
		TotalRequestsBefore: before.TotalRequests,
		TotalRequestsAfter:  after.TotalRequests,
		TotalFailuresBefore: before.TotalFailures,
		TotalFailuresAfter:  after.TotalFailures,
		LatencyP50Before:    before.LatencyP50,
		LatencyP50After:     after.LatencyP50,
		LatencyP95Before:    before.LatencyP95,
		LatencyP95After:     after.LatencyP95,
		LatencyP99Before:    before.LatencyP99,
		LatencyP99After:     after.LatencyP99,
	}
	seen := map[string]bool{}
	for code, afterCount := range after.PrimaryErrorCount {
		seen[code] = true
		beforeCount := before.PrimaryErrorCount[code]
		switch {
		case beforeCount == 0 && afterCount > 0:
			out.New = append(out.New, DiffEntry{
				ErrorCode: code,
				After:     afterCount,
				Delta:     afterCount,
			})
		case afterCount > beforeCount:
			out.Increased = append(out.Increased, DiffEntry{
				ErrorCode: code,
				Before:    beforeCount,
				After:     afterCount,
				Delta:     afterCount - beforeCount,
			})
		case afterCount < beforeCount:
			out.Decreased = append(out.Decreased, DiffEntry{
				ErrorCode: code,
				Before:    beforeCount,
				After:     afterCount,
				Delta:     afterCount - beforeCount,
			})
		}
	}
	for code, beforeCount := range before.PrimaryErrorCount {
		if seen[code] {
			continue
		}
		out.Removed = append(out.Removed, DiffEntry{
			ErrorCode: code,
			Before:    beforeCount,
			Delta:     -beforeCount,
		})
	}
	return out
}

type DiffEntry struct {
	ErrorCode string
	Before    int
	After     int
	Delta     int
}

type WindowDiff struct {
	New       []DiffEntry
	Removed   []DiffEntry
	Increased []DiffEntry
	Decreased []DiffEntry

	TotalRequestsBefore int
	TotalRequestsAfter  int
	TotalFailuresBefore int
	TotalFailuresAfter  int
	LatencyP50Before    int64
	LatencyP50After     int64
	LatencyP95Before    int64
	LatencyP95After     int64
	LatencyP99Before    int64
	LatencyP99After     int64
}

func DiffSummaries(before, after store.WindowSummary) WindowDiff {
	out := WindowDiff{
		TotalRequestsBefore: before.TotalRequests,
		TotalRequestsAfter:  after.TotalRequests,
		TotalFailuresBefore: before.TotalFailures,
		TotalFailuresAfter:  after.TotalFailures,
		LatencyP50Before:    before.LatencyP50,
		LatencyP50After:     after.LatencyP50,
		LatencyP95Before:    before.LatencyP95,
		LatencyP95After:     after.LatencyP95,
		LatencyP99Before:    before.LatencyP99,
		LatencyP99After:     after.LatencyP99,
	}

	seen := map[string]bool{}

	// Errors present in "after"
	for err, afterCount := range after.ErrorCount {
		seen[err] = true
		beforeCount := before.ErrorCount[err]

		switch {
		case beforeCount == 0 && afterCount > 0:
			out.New = append(out.New, DiffEntry{
				ErrorCode: err,
				After:     afterCount,
				Delta:     afterCount,
			})

		case afterCount > beforeCount:
			out.Increased = append(out.Increased, DiffEntry{
				ErrorCode: err,
				Before:    beforeCount,
				After:     afterCount,
				Delta:     afterCount - beforeCount,
			})

		case afterCount < beforeCount:
			out.Decreased = append(out.Decreased, DiffEntry{
				ErrorCode: err,
				Before:    beforeCount,
				After:     afterCount,
				Delta:     afterCount - beforeCount,
			})
		}
	}

	// Errors that disappeared
	for err, beforeCount := range before.ErrorCount {
		if seen[err] {
			continue
		}
		out.Removed = append(out.Removed, DiffEntry{
			ErrorCode: err,
			Before:    beforeCount,
			Delta:     -beforeCount,
		})
	}

	return out
}
