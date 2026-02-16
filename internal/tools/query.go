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

type queryInput struct {
	Expr   string `json:"expr"`
	Window string `json:"window"`
}

type queryOutput struct {
	MatchedRequests int `json:"matched_requests"`
}

func handleGraphQuery(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input queryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.Expr == "" {
		return nil, fmt.Errorf("expr required")
	}
	if input.Window == "" {
		return nil, fmt.Errorf("window required")
	}

	d, err := time.ParseDuration(input.Window)
	if err != nil {
		return nil, fmt.Errorf("invalid window: %w", err)
	}
	pred, err := query.Parse(input.Expr)
	if err != nil {
		return nil, fmt.Errorf("query parse error: %w", err)
	}

	end := time.Now()
	start := end.Add(-d)
	matched := 0

	store.ForEachRequestFact(start, end, func(f graphstore.RequestFacts) {
		if pred.Eval(f) {
			matched++
		}
	})

	return queryOutput{MatchedRequests: matched}, nil
}

type diffInput struct {
	Current  string `json:"current"`
	Baseline string `json:"baseline"`
	Offset   string `json:"offset"`
}

type diffEntry struct {
	ErrorCode string `json:"error_code"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
	Delta     int    `json:"delta"`
}

type diffOutput struct {
	New       []diffEntry `json:"new,omitempty"`
	Removed   []diffEntry `json:"removed,omitempty"`
	Increased []diffEntry `json:"increased,omitempty"`
	Decreased []diffEntry `json:"decreased,omitempty"`
}

func handleCompareWindows(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input diffInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.Current == "" || input.Baseline == "" || input.Offset == "" {
		return nil, fmt.Errorf("current, baseline, and offset required")
	}

	currDur, err := time.ParseDuration(input.Current)
	if err != nil {
		return nil, fmt.Errorf("invalid current: %w", err)
	}
	baseDur, err := time.ParseDuration(input.Baseline)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline: %w", err)
	}
	offDur, err := time.ParseDuration(input.Offset)
	if err != nil {
		return nil, fmt.Errorf("invalid offset: %w", err)
	}

	now := time.Now()
	currEnd := now
	currStart := currEnd.Add(-currDur)
	baseEnd := currEnd.Add(-offDur)
	baseStart := baseEnd.Add(-baseDur)

	curr := store.SummarizeWindow(currStart, currEnd)
	base := store.SummarizeWindow(baseStart, baseEnd)
	diff := analysis.DiffSummaries(base, curr)
	g := store.Snapshot()

	return diffOutput{
		New:       mapDiffEntries(g, diff.New),
		Removed:   mapDiffEntries(g, diff.Removed),
		Increased: mapDiffEntries(g, diff.Increased),
		Decreased: mapDiffEntries(g, diff.Decreased),
	}, nil
}
