package incidents

import (
	"context"
	"log/slog"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
)

// ResolvedPruner is the slice of the incident store the janitor needs.
type ResolvedPruner interface {
	PruneResolvedOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}

// RunRetention deletes resolved incidents older than retention on every
// interval tick. Active and recovering incidents are never touched (the
// store's DELETE filters on status=resolved). Mirrors signals.RunRetention.
func RunRetention(ctx context.Context, store ResolvedPruner, retention, interval time.Duration, m *metrics.Metrics, log *slog.Logger) {
	if store == nil || retention <= 0 || interval <= 0 {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().UTC().Add(-retention)
			deleted, err := store.PruneResolvedOlderThan(ctx, cutoff)
			if err != nil {
				log.Warn("incident retention prune failed", "err", err)
				continue
			}
			if m != nil && deleted > 0 {
				m.IncidentRetentionPruned.Add(float64(deleted))
			}
			if deleted > 0 {
				log.Info("incident retention pruned", "deleted", deleted, "cutoff", cutoff)
			}
		}
	}
}
