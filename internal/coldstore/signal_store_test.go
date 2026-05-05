package coldstore

import (
	"context"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
)

func TestSignalStoreInsertQueryAndPrune(t *testing.T) {
	store := newSignalTestStore(t)
	sigStore := NewSignalStore(store)
	base := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	rows := []signals.Signal{
		testSignal("sig_a", signals.TypeDeploy, "github", "checkout", "prod", "RolloutComplete", base.Add(-time.Minute)),
		testSignal("sig_b", signals.TypeDependency, "statuspage", "payment", "prod", "Provider5xx", base),
		testSignal("sig_c", signals.TypeDeploy, "github", "checkout", "staging", "RolloutComplete", base.Add(-2*time.Minute)),
	}
	for i := range rows {
		if err := sigStore.Insert(context.Background(), &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sigStore.Query(context.Background(), signals.Filter{
		Service: "checkout",
		Env:     "prod",
		Source:  "github",
		Reason:  "RolloutComplete",
		Types:   []signals.Type{signals.TypeDeploy},
		Since:   base.Add(-2 * time.Minute),
		Until:   base,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SignalID != "sig_a" {
		t.Fatalf("got=%+v", got)
	}
	if got[0].Metadata["version"] != "1.2.3" || got[0].Extra["custom_tag"] != "alpha" {
		t.Fatalf("metadata/extra not round-tripped: %+v", got[0])
	}

	got, err = sigStore.Query(context.Background(), signals.Filter{Env: "prod", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SignalID != "sig_b" || got[1].SignalID != "sig_a" {
		t.Fatalf("ordering got=%+v", got)
	}

	deleted, err := sigStore.PruneOlderThan(context.Background(), base.Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d want 2", deleted)
	}
	got, err = sigStore.Query(context.Background(), signals.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SignalID != "sig_b" {
		t.Fatalf("after prune got=%+v", got)
	}
}

func newSignalTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	store, ok := managed.(*SQLiteStore)
	if !ok {
		t.Fatalf("store type=%T", managed)
	}
	return store
}

func testSignal(id string, typ signals.Type, source, service, env, reason string, ts time.Time) signals.Signal {
	return signals.Signal{
		SignalID:   id,
		Type:       typ,
		Source:     source,
		Service:    service,
		Env:        env,
		Severity:   signals.SeverityInfo,
		Reason:     reason,
		Metadata:   map[string]any{"version": "1.2.3"},
		Extra:      map[string]any{"custom_tag": "alpha"},
		Timestamp:  ts,
		ReceivedAt: ts.Add(time.Second),
	}
}
