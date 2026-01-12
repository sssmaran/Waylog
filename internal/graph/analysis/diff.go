package analysis

import "github.com/sssmaran/WaylogCLI/internal/graph/store"


type DiffEntry struct {
	ErrorCode string
	Before    int
	After     int
	Delta     int
}

type WindowDiff struct {
	New        []DiffEntry
	Removed    []DiffEntry
	Increased  []DiffEntry
	Decreased  []DiffEntry
}
func DiffSummaries(before, after store.WindowSummary) WindowDiff {
	out := WindowDiff{}

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
