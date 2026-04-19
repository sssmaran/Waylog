package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/query"
)

// mapRollupDiffEntries converts DiffEntry rows that already carry canonical
// error codes (as produced by DiffRollups) into the tool-layer response shape.
// No id→code translation is needed.
func mapRollupDiffEntries(entries []analysis.DiffEntry) []diffEntry {
	out := make([]diffEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, diffEntry{
			ErrorCode: e.ErrorCode,
			Before:    e.Before,
			After:     e.After,
			Delta:     e.Delta,
		})
	}
	return out
}

type queryInput struct {
	Expr   string `json:"expr"`
	Window string `json:"window"`
}

type queryOutput struct {
	SchemaVersion   string `json:"schema_version"`
	MatchedRequests int    `json:"matched_requests"`
}

func handleGraphQuery(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input queryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.Expr == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "expr required"}
	}
	if input.Window == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "window required"}
	}

	d, err := time.ParseDuration(input.Window)
	if err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid window: %v", err)}
	}
	pred, err := query.Parse(input.Expr)
	if err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("query parse error: %v", err)}
	}

	end := time.Now()
	start := end.Add(-d)
	matched := 0

	store.ForEachRequestFact(start, end, func(f graphstore.RequestFacts) {
		if pred.Eval(f) {
			matched++
		}
	})

	return queryOutput{SchemaVersion: "1.0", MatchedRequests: matched}, nil
}

type diffInput struct {
	Current  string `json:"current"`
	Baseline string `json:"baseline"`
	Offset   string `json:"offset"`
	Anchor   string `json:"anchor"`
}

type diffEntry struct {
	ErrorCode string `json:"error_code"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
	Delta     int    `json:"delta"`
}

type diffOutput struct {
	SchemaVersion string      `json:"schema_version"`
	New           []diffEntry `json:"new,omitempty"`
	Removed       []diffEntry `json:"removed,omitempty"`
	Increased     []diffEntry `json:"increased,omitempty"`
	Decreased     []diffEntry `json:"decreased,omitempty"`

	TotalRequestsBefore int   `json:"total_requests_before"`
	TotalRequestsAfter  int   `json:"total_requests_after"`
	TotalFailuresBefore int   `json:"total_failures_before"`
	TotalFailuresAfter  int   `json:"total_failures_after"`
	LatencyP50Before    int64 `json:"latency_p50_before"`
	LatencyP50After     int64 `json:"latency_p50_after"`
	LatencyP95Before    int64 `json:"latency_p95_before"`
	LatencyP95After     int64 `json:"latency_p95_after"`
	LatencyP99Before    int64 `json:"latency_p99_before"`
	LatencyP99After     int64 `json:"latency_p99_after"`
}

func handleCompareWindows(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input diffInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.Current == "" || input.Baseline == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "current and baseline required"}
	}
	if input.Anchor == "" && input.Offset == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "either offset or anchor required"}
	}
	if input.Anchor != "" && input.Offset != "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "offset and anchor are mutually exclusive"}
	}

	currDur, err := time.ParseDuration(input.Current)
	if err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid current: %v", err)}
	}
	baseDur, err := time.ParseDuration(input.Baseline)
	if err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid baseline: %v", err)}
	}

	var currStart, currEnd, baseStart, baseEnd time.Time
	if input.Anchor != "" {
		anchor, parseErr := time.Parse(time.RFC3339, input.Anchor)
		if parseErr != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid anchor: %v", parseErr)}
		}
		currStart = anchor
		currEnd = anchor.Add(currDur)
		baseEnd = anchor
		baseStart = anchor.Add(-baseDur)
	} else {
		offDur, parseErr := time.ParseDuration(input.Offset)
		if parseErr != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid offset: %v", parseErr)}
		}
		now := time.Now()
		currEnd = now
		currStart = currEnd.Add(-currDur)
		baseEnd = currEnd.Add(-offDur)
		baseStart = baseEnd.Add(-baseDur)
	}

	g := store.Snapshot()
	ts := traceStoreFrom(store)
	curr := analysis.RollupWindow(g, store, ts, currStart, currEnd)
	base := analysis.RollupWindow(g, store, ts, baseStart, baseEnd)
	diff := analysis.DiffRollups(base, curr)

	return diffOutput{
		SchemaVersion:       "1.0",
		New:                 mapRollupDiffEntries(diff.New),
		Removed:             mapRollupDiffEntries(diff.Removed),
		Increased:           mapRollupDiffEntries(diff.Increased),
		Decreased:           mapRollupDiffEntries(diff.Decreased),
		TotalRequestsBefore: diff.TotalRequestsBefore,
		TotalRequestsAfter:  diff.TotalRequestsAfter,
		TotalFailuresBefore: diff.TotalFailuresBefore,
		TotalFailuresAfter:  diff.TotalFailuresAfter,
		LatencyP50Before:    diff.LatencyP50Before,
		LatencyP50After:     diff.LatencyP50After,
		LatencyP95Before:    diff.LatencyP95Before,
		LatencyP95After:     diff.LatencyP95After,
		LatencyP99Before:    diff.LatencyP99Before,
		LatencyP99After:     diff.LatencyP99After,
	}, nil
}
