package incidents

import (
	"context"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
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

func TestEngineUsesDownstreamDependencySignal(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily:    testFamily(),
			Count:          6,
			AffectedTraces: 6,
			SampleTraces:   []string{"trace-a"},
		}}},
		blast: apiv2.BlastRadiusResponse{AffectedRequests: 6, AffectedServices: 2, TopServices: []string{"checkout", "payment"}},
		events: []*eventv2.Event{
			testIncidentEvent("e1", "trace-a", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "payment"),
		},
	}
	signalStore := &fakeSignalStore{rows: []signals.Signal{{
		SignalID:  "sig_payment",
		Type:      signals.TypeDependency,
		Service:   "payment",
		Env:       "prod",
		Severity:  signals.SeverityCritical,
		Reason:    "payment_gateway_5xx",
		Timestamp: now.Add(-2 * time.Minute),
	}}}
	engine := NewEngine(reader, signalStore, nil, NewMemoryStore(), Config{MinCount: 5, SampleLimit: 2}, nil, nil)
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
	if len(rows) != 1 {
		t.Fatalf("active incidents = %d, want 1", len(rows))
	}
	if rows[0].Cause != CauseDependency || rows[0].Confidence != ConfidenceHigh {
		t.Fatalf("classification = %s/%s, want dependency/high", rows[0].Cause, rows[0].Confidence)
	}
	if len(signalStore.filters) < 1 || signalStore.filters[0].Service != "" || signalStore.filters[0].Env != "prod" {
		t.Fatalf("signal filters = %+v", signalStore.filters)
	}
}

func TestDerivePreservesSeedContinuity(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	started := now.Add(-20 * time.Minute)
	seeded := testIncident(started)
	seeded.SampleTraces = []string{"trace-seeded"}
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily: testFamily(),
			Count:       6,
		}}},
		blast: apiv2.BlastRadiusResponse{AffectedRequests: 6, AffectedServices: 2, TopServices: []string{"checkout", "payment"}},
		events: []*eventv2.Event{
			testIncidentEvent("new", "trace-new", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "payment"),
		},
	}
	engine := NewEngine(reader, nil, nil, NewMemoryStore(), Config{MinCount: 5, SampleLimit: 2}, nil, nil)
	rows, err := engine.derive(context.Background(), now, map[string]Incident{seeded.IncidentID: seeded}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	got := rows[0].Incident
	if !got.StartedAt.Equal(started) {
		t.Fatalf("started_at=%s want %s", got.StartedAt, started)
	}
	if len(got.SampleTraces) != 2 || got.SampleTraces[0] != "trace-seeded" || got.SampleTraces[1] != "trace-new" {
		t.Fatalf("sample_traces=%+v", got.SampleTraces)
	}
	if !rows[0].Existed {
		t.Fatalf("seeded row should be marked existed")
	}
}

func TestDeriveMissingTransitions(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{}
	engine := NewEngine(reader, nil, nil, NewMemoryStore(), Config{ResolveAfter: time.Minute}, nil, nil)

	active := testIncident(now.Add(-5 * time.Minute))
	rows, err := engine.derive(context.Background(), now, map[string]Incident{active.IncidentID: active}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Incident.Status != StatusRecovering {
		t.Fatalf("active missing rows=%+v", rows)
	}

	recovering := testIncident(now.Add(-5 * time.Minute))
	recovering.Status = StatusRecovering
	recovering.LastSeenAt = now.Add(-2 * time.Minute)
	rows, err = engine.derive(context.Background(), now, map[string]Incident{recovering.IncidentID: recovering}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Incident.Status != StatusResolved {
		t.Fatalf("recovering missing rows=%+v", rows)
	}
}

func TestApplyRebuildReplacesStoreAndReloadsCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	oldActive := testIncident(now.Add(-10 * time.Minute))
	resolved := testIncident(now.Add(-20 * time.Minute))
	resolved.IncidentID = "inc_resolved"
	resolved.Status = StatusResolved
	resolvedAt := now.Add(-5 * time.Minute)
	resolved.ResolvedAt = &resolvedAt
	if err := store.Upsert(ctx, oldActive); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(&fakeReader{}, nil, nil, store, Config{}, nil, nil)
	if err := engine.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	newActive := testIncident(now)
	newActive.IncidentID = "inc_new"
	if err := engine.ApplyRebuild(ctx, []derivedRow{{Incident: newActive}}); err != nil {
		t.Fatal(err)
	}
	active, err := engine.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].IncidentID != "inc_new" {
		t.Fatalf("active after rebuild=%+v", active)
	}
	if _, ok := engine.SnapshotActive()["inc_new"]; !ok {
		t.Fatalf("cache was not reloaded from rebuilt rows")
	}
	if _, err := store.Get(ctx, "inc_resolved"); err != nil {
		t.Fatalf("resolved row should be preserved: %v", err)
	}
	if _, err := store.Get(ctx, oldActive.IncidentID); err == nil {
		t.Fatalf("old non-resolved row should be replaced")
	}
}

func TestRebuildOrchestratorUsesRebuildApply(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily: testFamily(),
			Count:       6,
		}}},
		blast: apiv2.BlastRadiusResponse{AffectedRequests: 6, AffectedServices: 2},
		events: []*eventv2.Event{
			testIncidentEvent("new", "trace-new", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "payment"),
		},
	}
	engine := NewEngine(reader, nil, nil, NewMemoryStore(), Config{MinCount: 5, SampleLimit: 2}, nil, nil)
	result, err := Rebuild(ctx, RebuildDeps{Engine: engine, Reader: reader, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsReplaced != 1 {
		t.Fatalf("rows_replaced=%d", result.RowsReplaced)
	}
	if len(engine.SnapshotActive()) != 1 {
		t.Fatalf("cache should reflect rebuilt active rows")
	}
}

type fakeReader struct {
	current     ErrorsResult
	base        ErrorsResult
	blast       apiv2.BlastRadiusResponse
	events      []*eventv2.Event
	calls       int
	story       apiv2.StoryResponse
	storyOK     bool
	traceEvts   []*eventv2.Event
	traceEvtsOK bool
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

func (r *fakeReader) TraceStoryByTraceID(_ string) (apiv2.StoryResponse, bool) {
	return r.story, r.storyOK
}

func (r *fakeReader) TraceEvents(_ string) ([]*eventv2.Event, bool) {
	return r.traceEvts, r.traceEvtsOK
}

type fakeSignalStore struct {
	rows    []signals.Signal
	filters []signals.Filter
}

func (s *fakeSignalStore) Query(_ context.Context, f signals.Filter) ([]signals.Signal, error) {
	s.filters = append(s.filters, f)
	return s.rows, nil
}

func TestEngine_PropagationOpeningSurvivesAcrossTicks(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	fam := testFamily()

	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily:    fam,
			Count:          6,
			AffectedTraces: 6,
			SampleTraces:   []string{"trace_a"},
		}}},
		blast: apiv2.BlastRadiusResponse{
			AffectedRequests: 3,
			AffectedServices: 2,
			SampleTraces:     []string{"trace_a"},
			TopServices:      []string{"checkout"},
		},
		events: []*eventv2.Event{
			testIncidentEvent("anchor", "trace_a", now.Add(-time.Minute),
				"checkout", fam.Step, fam.ErrorCode, fam.Service),
		},
		story: apiv2.StoryResponse{
			Service: fam.Service,
			Anchor:  &apiv2.StoryAnchor{Step: fam.Step},
			Path:    []apiv2.StoryStep{{Name: fam.Step, Status: "error", ErrorCode: fam.ErrorCode}},
		},
		storyOK: true,
		traceEvts: []*eventv2.Event{{
			TsStart: now.Add(-90 * time.Second),
			Anchor:  &eventv2.Anchor{Step: fam.Step, ErrorCode: fam.ErrorCode},
		}},
		traceEvtsOK: true,
	}
	store := NewMemoryStore()
	engine := NewEngine(reader, nil, nil, store, Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2}, nil, nil)
	engine.now = func() time.Time { return now }
	ctx := context.Background()
	if err := engine.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	actives, err := engine.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(actives) != 1 {
		t.Fatalf("expected 1 active incident, got %d", len(actives))
	}
	incID := actives[0].IncidentID
	if actives[0].Propagation == nil || actives[0].Propagation.Opening == nil {
		t.Fatalf("Propagation.Opening should be set after tick 1: %+v", actives[0].Propagation)
	}
	if actives[0].Blast == nil || actives[0].Blast.Opening == nil {
		t.Fatalf("Blast.Opening should be set after tick 1: %+v", actives[0].Blast)
	}

	// Tick 2: blast still OK, but no sample traces -> propagation missing.
	// Opening should carry forward through the engine merge + store persistence.
	reader.blast.SampleTraces = nil
	reader.storyOK = false
	reader.traceEvtsOK = false
	now = now.Add(30 * time.Second)
	engine.now = func() time.Time { return now }
	if err := engine.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, incID)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if got.Propagation == nil || got.Propagation.Opening == nil {
		t.Fatalf("Propagation.Opening lost after tick 2: %+v", got.Propagation)
	}
	if got.Propagation.Latest == nil || got.Propagation.Latest.CaptureStatus != CaptureMissing {
		t.Errorf("Propagation.Latest should be missing: %+v", got.Propagation.Latest)
	}
	if got.Blast == nil || got.Blast.Opening == nil {
		t.Errorf("Blast.Opening should still be set: %+v", got.Blast)
	}
}
