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

// stubNotifier records lifecycle calls for the notification-hook test.
type stubNotifier struct {
	opened   []string
	resolved []string
}

func (s *stubNotifier) IncidentOpened(inc Incident) { s.opened = append(s.opened, inc.IncidentID) }
func (s *stubNotifier) IncidentResolved(inc Incident) {
	s.resolved = append(s.resolved, inc.IncidentID)
}

// TestEngineNotifiesOnOpenAndResolveOnce proves the engine→notifier seam: open
// fires exactly once (not on per-tick updates), and resolve fires on resolution.
func TestEngineNotifiesOnOpenAndResolveOnce(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily: testFamily(), Count: 6, AffectedTraces: 6, SampleTraces: []string{"t1"},
		}}},
		blast:  apiv2.BlastRadiusResponse{AffectedRequests: 6, AffectedServices: 2, TopServices: []string{"checkout"}},
		events: []*eventv2.Event{testIncidentEvent("e", "t1", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "")},
	}
	store := NewMemoryStore()
	engine := NewEngine(reader, nil, nil, store, Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2}, nil, nil)
	stub := &stubNotifier{}
	engine.SetNotifier(stub)
	engine.now = func() time.Time { return now }
	if err := engine.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Tick 1: opens → one IncidentOpened.
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.opened) != 1 {
		t.Fatalf("expected 1 open notification, got %d", len(stub.opened))
	}

	// Tick 2: same family still firing → an UPDATE, not a re-open. No new notification.
	now = now.Add(10 * time.Second)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.opened) != 1 {
		t.Fatalf("update tick must not re-notify open, got %d", len(stub.opened))
	}

	// Stop the family, advance past ResolveAfter → recovering then resolved.
	reader.current.Rows = nil
	now = now.Add(30 * time.Second)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.resolved) != 1 {
		t.Fatalf("expected 1 resolve notification, got %d", len(stub.resolved))
	}
	if stub.opened[0] != stub.resolved[0] {
		t.Fatalf("open/resolve incident IDs differ: %q vs %q", stub.opened[0], stub.resolved[0])
	}
}

// trafficCfg is the standard opt-in traffic config for tests (sustained=1 unless
// a test overrides, so a single tick opens — the sustained gate has its own test).
func trafficCfg() TrafficAnomalyConfig {
	return TrafficAnomalyConfig{Enabled: true, DropFactor: 0.5, SurgeFactor: 3.0, MinVolume: 20, SustainedTicks: 1}
}

func newTrafficEngine(t *testing.T, reader *fakeReader, cfg TrafficAnomalyConfig, now time.Time) *Engine {
	t.Helper()
	eng := NewEngine(reader, nil, nil, NewMemoryStore(), Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2, TrafficAnomaly: cfg}, nil, nil)
	eng.now = func() time.Time { return now }
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	return eng
}

func trafficReader(current, baseline int) *fakeReader {
	return &fakeReader{
		// No error rows — pure traffic scenario.
		statCurrent: []ServiceStatsRow{{Service: "checkout", Count: current}},
		statBase:    []ServiceStatsRow{{Service: "checkout", Count: baseline}},
		events:      []*eventv2.Event{testIncidentEvent("e", "t", time.Now(), "checkout", "request", "", "")},
	}
}

func activeTraffic(t *testing.T, eng *Engine) Incident {
	t.Helper()
	rows, err := eng.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ErrorFamily.Step == trafficStep {
			return r
		}
	}
	return Incident{}
}

func TestTrafficDropOpensIncident(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// baseline 100, current 10 → 10 <= 0.5*100 → drop.
	eng := newTrafficEngine(t, trafficReader(10, 100), trafficCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	inc := activeTraffic(t, eng)
	if inc.ErrorFamily.ErrorCode != errorCodeTrafficDrop {
		t.Fatalf("want TRAFFIC_DROP incident, got %+v", inc.ErrorFamily)
	}
	if inc.CurrentCount != 10 || inc.BaselineCount != 100 {
		t.Fatalf("counts = current %d baseline %d", inc.CurrentCount, inc.BaselineCount)
	}
}

func TestTrafficSurgeOpensIncident(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// baseline 100, current 400 → 400 >= 3*100 → surge.
	eng := newTrafficEngine(t, trafficReader(400, 100), trafficCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.ErrorFamily.ErrorCode != errorCodeTrafficSurge {
		t.Fatalf("want TRAFFIC_SURGE incident, got %+v", inc.ErrorFamily)
	}
}

func TestTrafficMinVolumeFloorSuppressesLowTrafficService(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// baseline 5 < MinVolume 20 → never flagged even though current 0 is a "drop".
	eng := newTrafficEngine(t, trafficReader(0, 5), trafficCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.IncidentID != "" {
		t.Fatalf("low-traffic service must not flag, got %+v", inc.ErrorFamily)
	}
}

func TestTrafficColdStartNeverFlagsDrop(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// No prior traffic (baseline 0) → ineligible (floor), even with current 0.
	eng := newTrafficEngine(t, trafficReader(0, 0), trafficCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.IncidentID != "" {
		t.Fatalf("cold-start service must not flag a drop, got %+v", inc.ErrorFamily)
	}
}

func TestTrafficSustainedGateRequiresTwoTicks(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	cfg := trafficCfg()
	cfg.SustainedTicks = 2
	eng := newTrafficEngine(t, trafficReader(10, 100), cfg, now)

	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.IncidentID != "" {
		t.Fatalf("single anomalous tick must not open, got %+v", inc.ErrorFamily)
	}
	now = now.Add(10 * time.Second)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.ErrorFamily.ErrorCode != errorCodeTrafficDrop {
		t.Fatalf("second consecutive tick must open, got %+v", inc.ErrorFamily)
	}
}

func TestTrafficDisabledByDefault(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// Enabled:false → no traffic incident even under a clear drop.
	eng := newTrafficEngine(t, trafficReader(0, 100), TrafficAnomalyConfig{}, now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.IncidentID != "" {
		t.Fatalf("traffic detection must be off by default, got %+v", inc.ErrorFamily)
	}
}

func TestTrafficDropRecoversWhenVolumeReturns(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := trafficReader(10, 100)
	eng := newTrafficEngine(t, reader, trafficCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.Status != StatusActive {
		t.Fatalf("expected active drop, got %+v", inc)
	}
	// Volume returns to normal → anomaly clears → recovering via the shared path.
	reader.statCurrent = []ServiceStatsRow{{Service: "checkout", Count: 100}}
	now = now.Add(10 * time.Second)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeTraffic(t, eng); inc.Status != StatusRecovering {
		t.Fatalf("expected recovering after volume returns, got %+v", inc)
	}
}

func TestTrafficDropCorrelatesDeploy(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := trafficReader(10, 100)
	deploys := &fakeDeploySource{deps: []Deployment{{
		ID: "dep_x", Service: "checkout", Env: "prod", FirstSeen: now.Add(-2 * time.Minute),
	}}}
	eng := NewEngine(reader, nil, deploys, NewMemoryStore(),
		Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2, DeployCorrelationWindow: 15 * time.Minute, TrafficAnomaly: trafficCfg()}, nil, nil)
	eng.now = func() time.Time { return now }
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	inc := activeTraffic(t, eng)
	if inc.Cause != CauseDeploy {
		t.Fatalf("traffic drop after a deploy should classify cause=deploy, got %q", inc.Cause)
	}
	if inc.SuspectDeployID != "dep_x" {
		t.Fatalf("suspect deploy should be dep_x, got %q", inc.SuspectDeployID)
	}
}

func TestTrafficDetectionIsDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	build := func() Incident {
		eng := newTrafficEngine(t, trafficReader(10, 100), trafficCfg(), now)
		if err := eng.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		return activeTraffic(t, eng)
	}
	a, b := build(), build()
	if a.IncidentID != b.IncidentID || a.ErrorFamily != b.ErrorFamily || a.CurrentCount != b.CurrentCount || a.BaselineCount != b.BaselineCount {
		t.Fatalf("traffic detection not deterministic:\n a=%+v\n b=%+v", a, b)
	}
}

// --- latency anomaly ---

func latencyCfg() LatencyAnomalyConfig {
	return LatencyAnomalyConfig{Enabled: true, Percentile: 95, Factor: 2.0, MinRequests: 50, MinMS: 0, SustainedTicks: 1}
}

// latencyReader: current pX + baseline pX (same value for all 3 baseline windows),
// each reported with `samples` so the MinRequests floor can be exercised.
func latencyReader(currentMS, baselineMS int64, samples int) *fakeReader {
	return &fakeReader{
		statCurrent: []ServiceStatsRow{{Service: "checkout", Count: samples, LatencyMS: currentMS}},
		statBase:    []ServiceStatsRow{{Service: "checkout", Count: samples, LatencyMS: baselineMS}},
		events:      []*eventv2.Event{testIncidentEvent("e", "t", time.Now(), "checkout", "request", "", "")},
	}
}

func newLatencyEngine(t *testing.T, reader *fakeReader, cfg LatencyAnomalyConfig, now time.Time) *Engine {
	t.Helper()
	eng := NewEngine(reader, nil, nil, NewMemoryStore(), Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2, LatencyAnomaly: cfg}, nil, nil)
	eng.now = func() time.Time { return now }
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	return eng
}

func activeLatency(t *testing.T, eng *Engine) Incident {
	t.Helper()
	rows, err := eng.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ErrorFamily.Step == latencyStep {
			return r
		}
	}
	return Incident{}
}

func TestLatencySpikeOpensIncident(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// baseline 100ms, current 300ms → 300 >= 2*100 → spike.
	eng := newLatencyEngine(t, latencyReader(300, 100, 200), latencyCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	inc := activeLatency(t, eng)
	if inc.ErrorFamily.ErrorCode != errorCodeLatencySpike {
		t.Fatalf("want LATENCY_SPIKE, got %+v", inc.ErrorFamily)
	}
	if inc.CurrentCount != 300 || inc.BaselineCount != 100 {
		t.Fatalf("ms current %d baseline %d", inc.CurrentCount, inc.BaselineCount)
	}
}

func TestLatencySampleFloorSuppresses(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	// Clear 3x spike, but only 10 samples/window < MinRequests 50 → ineligible.
	eng := newLatencyEngine(t, latencyReader(300, 100, 10), latencyCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.IncidentID != "" {
		t.Fatalf("below MinRequests must not flag, got %+v", inc.ErrorFamily)
	}
}

func TestLatencyAbsoluteFloorSuppresses(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	cfg := latencyCfg()
	cfg.MinMS = 250 // current 9ms is a 3x ratio but below the absolute floor.
	eng := newLatencyEngine(t, latencyReader(9, 3, 200), cfg, now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.IncidentID != "" {
		t.Fatalf("below MinMS must not flag, got %+v", inc.ErrorFamily)
	}
}

func TestLatencySustainedGateRequiresTwoTicks(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	cfg := latencyCfg()
	cfg.SustainedTicks = 2
	eng := newLatencyEngine(t, latencyReader(300, 100, 200), cfg, now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.IncidentID != "" {
		t.Fatalf("single tick must not open, got %+v", inc.ErrorFamily)
	}
	now = now.Add(10 * time.Second)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.ErrorFamily.ErrorCode != errorCodeLatencySpike {
		t.Fatalf("second tick must open, got %+v", inc.ErrorFamily)
	}
}

func TestLatencyDisabledByDefault(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	eng := newLatencyEngine(t, latencyReader(300, 100, 200), LatencyAnomalyConfig{}, now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.IncidentID != "" {
		t.Fatalf("latency detection must be off by default, got %+v", inc.ErrorFamily)
	}
}

func TestLatencyRecoversWhenLatencyReturns(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := latencyReader(300, 100, 200)
	eng := newLatencyEngine(t, reader, latencyCfg(), now)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.Status != StatusActive {
		t.Fatalf("expected active spike, got %+v", inc)
	}
	reader.statCurrent = []ServiceStatsRow{{Service: "checkout", Count: 200, LatencyMS: 100}}
	now = now.Add(10 * time.Second)
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inc := activeLatency(t, eng); inc.Status != StatusRecovering {
		t.Fatalf("expected recovering after latency returns, got %+v", inc)
	}
}

func TestLatencySpikeCorrelatesDeploy(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := latencyReader(300, 100, 200)
	deploys := &fakeDeploySource{deps: []Deployment{{ID: "dep_l", Service: "checkout", Env: "prod", FirstSeen: now.Add(-2 * time.Minute)}}}
	eng := NewEngine(reader, nil, deploys, NewMemoryStore(),
		Config{MinCount: 5, ResolveAfter: time.Minute, SampleLimit: 2, DeployCorrelationWindow: 15 * time.Minute, LatencyAnomaly: latencyCfg()}, nil, nil)
	eng.now = func() time.Time { return now }
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := eng.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	inc := activeLatency(t, eng)
	if inc.Cause != CauseDeploy || inc.SuspectDeployID != "dep_l" {
		t.Fatalf("latency spike after deploy should be cause=deploy/dep_l, got cause=%q suspect=%q", inc.Cause, inc.SuspectDeployID)
	}
}

func TestLatencyDetectionIsDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	build := func() Incident {
		eng := newLatencyEngine(t, latencyReader(300, 100, 200), latencyCfg(), now)
		if err := eng.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		return activeLatency(t, eng)
	}
	a, b := build(), build()
	if a.IncidentID != b.IncidentID || a.ErrorFamily != b.ErrorFamily || a.CurrentCount != b.CurrentCount || a.BaselineCount != b.BaselineCount {
		t.Fatalf("latency detection not deterministic:\n a=%+v\n b=%+v", a, b)
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
	base        ErrorsResult   // default for every baseline window
	baseSeq     []ErrorsResult // optional per-window baselines, newest prior window first
	blast       apiv2.BlastRadiusResponse
	events      []*eventv2.Event
	calls       int
	story       apiv2.StoryResponse
	storyOK     bool
	traceEvts   []*eventv2.Event
	traceEvtsOK bool

	statCurrent []ServiceStatsRow // current-window stats
	statBase    []ServiceStatsRow // stats returned for each baseline window
	statCalls   int
}

// ServiceStats mirrors derive's order: current window, then three baseline
// windows, repeating per tick (independent of the Errors call counter).
func (r *fakeReader) ServiceStats(_ SearchFilter, _ int, _ int) []ServiceStatsRow {
	pos := r.statCalls % 4
	r.statCalls++
	if pos == 0 {
		return r.statCurrent
	}
	return r.statBase
}

// Errors mirrors derive's query order: one current-window call followed by
// three baseline-window calls (newest prior window first), repeating per tick.
func (r *fakeReader) Errors(_ SearchFilter, _ int) ErrorsResult {
	pos := r.calls % 4
	r.calls++
	if pos == 0 {
		return r.current
	}
	if len(r.baseSeq) >= pos {
		return r.baseSeq[pos-1]
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

func spikeReader(currentCount int, baselineCounts [3]int) *fakeReader {
	row := func(n int) ErrorsResult {
		if n == 0 {
			return ErrorsResult{}
		}
		return ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily: testFamily(), Count: n, AffectedTraces: n, SampleTraces: []string{"trace-a"},
		}}}
	}
	return &fakeReader{
		current: row(currentCount),
		baseSeq: []ErrorsResult{row(baselineCounts[0]), row(baselineCounts[1]), row(baselineCounts[2])},
		blast:   apiv2.BlastRadiusResponse{AffectedRequests: currentCount, AffectedServices: 1},
	}
}

func activeAfterTick(t *testing.T, reader *fakeReader, cfg Config) []Incident {
	t.Helper()
	engine := NewEngine(reader, nil, nil, NewMemoryStore(), cfg, nil, nil)
	engine.now = func() time.Time { return time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC) }
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
	return rows
}

func TestBaselineMedianResistsOneSpikyPriorWindow(t *testing.T) {
	// One anomalous prior window (12) must not suppress a real spike: the
	// median of [12, 0, 0] is 0, so the family is treated as a fresh spike.
	rows := activeAfterTick(t, spikeReader(12, [3]int{12, 0, 0}), Config{MinCount: 5, MinLift: 3, SampleLimit: 2})
	if len(rows) != 1 {
		t.Fatalf("spiky baseline window suppressed a real incident: %+v", rows)
	}
	if rows[0].BaselineCount != 0 {
		t.Fatalf("baseline must be the median (0), got %d", rows[0].BaselineCount)
	}
}

func TestBaselineMedianSuppressesSteadyNoise(t *testing.T) {
	// A steadily failing family (~10/window) with current 12 has lift 1.2 < 3:
	// no incident.
	rows := activeAfterTick(t, spikeReader(12, [3]int{10, 9, 11}), Config{MinCount: 5, MinLift: 3, SampleLimit: 2})
	if len(rows) != 0 {
		t.Fatalf("steady error noise must not open an incident: %+v", rows)
	}
}

func TestMinRateGuardSuppressesLowTraffic(t *testing.T) {
	// 6 failures in a 10m window = 0.6/min. With MIN_RATE=1 the family must
	// not open; with the guard disabled (0) it must.
	cfg := Config{MinCount: 5, MinLift: 3, SampleLimit: 2, Window: 10 * time.Minute, MinRate: 1}
	if rows := activeAfterTick(t, spikeReader(6, [3]int{0, 0, 0}), cfg); len(rows) != 0 {
		t.Fatalf("min-rate guard must suppress low-traffic family: %+v", rows)
	}
	cfg.MinRate = 0
	if rows := activeAfterTick(t, spikeReader(6, [3]int{0, 0, 0}), cfg); len(rows) != 1 {
		t.Fatalf("disabled min-rate guard must preserve current behavior: %+v", rows)
	}
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
