package signals

import (
	"context"
	"log/slog"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
)

func RunRetention(ctx context.Context, store Store, retention, interval time.Duration, m *metrics.Metrics, log *slog.Logger) {
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
			deleted, err := store.PruneOlderThan(ctx, cutoff)
			if err != nil {
				log.Warn("signals retention prune failed", "err", err)
				continue
			}
			if m != nil && deleted > 0 {
				m.SignalRetentionPruned.Add(float64(deleted))
			}
			if deleted > 0 {
				log.Info("signals retention pruned", "deleted", deleted, "cutoff", cutoff)
			}
		}
	}
}
