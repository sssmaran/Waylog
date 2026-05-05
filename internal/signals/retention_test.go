package signals

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRetentionPrunesAndStops(t *testing.T) {
	store := &retentionStore{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunRetention(ctx, store, time.Minute, time.Millisecond, nil, slog.Default())
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		if store.calls() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("retention did not call prune")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention did not stop")
	}
}

type retentionStore struct {
	n atomic.Int64
}

func (s *retentionStore) Insert(context.Context, *Signal) error { return nil }
func (s *retentionStore) Query(context.Context, Filter) ([]Signal, error) {
	return nil, nil
}
func (s *retentionStore) PruneOlderThan(context.Context, time.Time) (int, error) {
	s.n.Add(1)
	return 1, nil
}
func (s *retentionStore) calls() int { return int(s.n.Load()) }
