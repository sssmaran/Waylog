package incidents

import (
	"context"
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestEngineLifecycleAndSampleStability(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily:    testFamily(),
			Count:          6,
			AffectedTraces: 6,
			SampleTraces:   []string{"trace-new"},
		}}},
		blast: apiv2.BlastRadiusResponse{
			AffectedRequests: 6,
			AffectedServices: 2,
			TopServices:      []string{"checkout", "payment"},
			SampleTraces:     []string{"trace-new"},
		},
		events: []*eventv2.Event{
			testIncidentEvent("old", "trace-old", now.Add(-2*time.Minute), "checkout", "payment.charge", "PMT_502", "payment"),
			testIncidentEvent("new", "trace-new", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "payment"),
		},
	}
	store := NewMemoryStore()
	engine := NewEngine(reader, nil, nil, store, Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2}, nil, nil)
	engine.now = func() time.Time { return now }
	if err := engine.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := engine.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != StatusActive {
		t.Fatalf("rows=%+v", rows)
	}
	if got := rows[0].SampleTraces; len(got) != 2 || got[0] != "trace-old" || got[1] != "trace-new" {
		t.Fatalf("samples=%+v", got)
	}

	reader.current.Rows = nil
	now = now.Add(30 * time.Second)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ = engine.Active(context.Background())
	if len(rows) != 1 || rows[0].Status != StatusRecovering {
		t.Fatalf("recovering rows=%+v", rows)
	}

	now = now.Add(2 * time.Minute)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ = engine.Active(context.Background())
	if len(rows) != 0 {
		t.Fatalf("expected resolved incident removed from active cache, rows=%+v", rows)
	}

	rehydrated := NewEngine(reader, nil, nil, store, Config{}, nil, nil)
	if err := rehydrated.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ = rehydrated.Active(context.Background())
	if len(rows) != 0 {
		t.Fatalf("bootstrap should ignore resolved incidents, rows=%+v", rows)
	}
}

type fakeReader struct {
	current ErrorsResult
	base    ErrorsResult
	blast   apiv2.BlastRadiusResponse
	events  []*eventv2.Event
	calls   int
}

func (r *fakeReader) Errors(_ SearchFilter, _ int) ErrorsResult {
	r.calls++
	if r.calls%2 == 1 {
		return r.current
	}
	return r.base
}

func (r *fakeReader) BlastRadius(_ SearchFilter, key apiv2.BlastKey) apiv2.BlastRadiusResponse {
	out := r.blast
	out.Key = key
	return out
}

func (r *fakeReader) SearchEvents(_ SearchFilter, _ int) []*eventv2.Event {
	return r.events
}
