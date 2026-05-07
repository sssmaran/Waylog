package incidents

import (
	"context"
	"fmt"
	"time"
)

type RebuildDeps struct {
	Engine *Engine
	Reader Reader
	Now    func() time.Time
}

type RebuildResult struct {
	RowsReplaced int
	Duration     time.Duration
}

func Rebuild(ctx context.Context, deps RebuildDeps) (RebuildResult, error) {
	if deps.Engine == nil {
		return RebuildResult{}, fmt.Errorf("incidents rebuild: engine required")
	}
	if deps.Reader == nil {
		return RebuildResult{}, fmt.Errorf("incidents rebuild: reader required")
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	start := time.Now()
	rows, err := deps.Engine.derive(ctx, nowFn().UTC(), deps.Engine.SnapshotActive(), deps.Reader)
	if err != nil {
		return RebuildResult{}, err
	}
	if err := deps.Engine.ApplyRebuild(ctx, rows); err != nil {
		return RebuildResult{}, err
	}
	return RebuildResult{RowsReplaced: len(rows), Duration: time.Since(start)}, nil
}
