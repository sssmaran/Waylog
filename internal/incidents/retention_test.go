package incidents

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakePruneStore struct {
	n          atomic.Int64
	lastCutoff atomic.Value // time.Time
}

func (s *fakePruneStore) PruneResolvedOlderThan(_ context.Context, cutoff time.Time) (int, error) {
	s.n.Add(1)
	s.lastCutoff.Store(cutoff)
	return 2, nil
}

func TestRunRetentionPrunesResolvedAndStops(t *testing.T) {
	store := &fakePruneStore{}
	retention := time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunRetention(ctx, store, retention, time.Millisecond, nil, slog.Default())
		close(done)
	}()

	deadline := time.After(time.Second)
	for store.n.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("retention did not call prune")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cutoff, _ := store.lastCutoff.Load().(time.Time)
	want := time.Now().UTC().Add(-retention)
	if d := want.Sub(cutoff); d < -time.Minute || d > time.Minute {
		t.Fatalf("cutoff %v not ~now-retention %v", cutoff, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention did not stop on context cancel")
	}
}

func TestRunRetentionDisabledConfigsReturnImmediately(t *testing.T) {
	store := &fakePruneStore{}
	done := make(chan struct{})
	go func() {
		RunRetention(context.Background(), nil, time.Hour, time.Millisecond, nil, nil)
		RunRetention(context.Background(), store, 0, time.Millisecond, nil, nil)
		RunRetention(context.Background(), store, time.Hour, 0, nil, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled retention configs must return immediately")
	}
	if store.n.Load() != 0 {
		t.Fatalf("disabled configs must never prune, got %d calls", store.n.Load())
	}
}
