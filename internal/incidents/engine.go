package incidents

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type Config struct {
	TickInterval            time.Duration
	Window                  time.Duration
	MinCount                int
	MinLift                 float64
	MinRate                 float64 // errors/minute low-traffic guard; 0 disables
	ResolveAfter            time.Duration
	DeployCorrelationWindow time.Duration
	SampleLimit             int
	TrafficAnomaly          TrafficAnomalyConfig
	LatencyAnomaly          LatencyAnomalyConfig
}

// TrafficAnomalyConfig configures the volume-anomaly detector. Opt-in; all
// thresholds are caller-supplied (defaults live at config load in cmd/ingest).
type TrafficAnomalyConfig struct {
	Enabled        bool
	DropFactor     float64 // flag drop when current <= DropFactor * baseline
	SurgeFactor    float64 // flag surge when current >= SurgeFactor * baseline; 0 disables surge
	MinVolume      int     // minimum baseline req/window for a service to be eligible
	SustainedTicks int     // consecutive anomalous ticks required before opening
}

// LatencyAnomalyConfig configures the tail-latency spike detector. Opt-in;
// defaults live at config load in cmd/ingest.
type LatencyAnomalyConfig struct {
	Enabled        bool
	Percentile     int     // tail percentile to track (1-99)
	Factor         float64 // flag when current >= Factor * baseline
	MinRequests    int     // min samples/window for a meaningful percentile
	MinMS          int64   // absolute floor on current pX; 0 disables
	SustainedTicks int     // consecutive anomalous ticks required before opening
}

func DefaultConfig() Config {
	return Config{
		TickInterval:            30 * time.Second,
		Window:                  10 * time.Minute,
		MinCount:                5,
		MinLift:                 3.0,
		ResolveAfter:            2 * time.Minute,
		DeployCorrelationWindow: 15 * time.Minute,
		SampleLimit:             5,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.TickInterval <= 0 {
		c.TickInterval = d.TickInterval
	}
	if c.Window <= 0 {
		c.Window = d.Window
	}
	if c.MinCount <= 0 {
		c.MinCount = d.MinCount
	}
	if c.MinLift <= 0 {
		c.MinLift = d.MinLift
	}
	if c.ResolveAfter <= 0 {
		c.ResolveAfter = d.ResolveAfter
	}
	if c.DeployCorrelationWindow <= 0 {
		c.DeployCorrelationWindow = d.DeployCorrelationWindow
	}
	if c.SampleLimit <= 0 {
		c.SampleLimit = d.SampleLimit
	}
	return c
}

// Notifier receives incident lifecycle transitions for outbound notification
// (Slack, PagerDuty, generic webhook). Implementations must be non-blocking and
// best-effort: a notify call never blocks or fails an engine tick. Optional.
type Notifier interface {
	IncidentOpened(inc Incident)
	IncidentResolved(inc Incident)
}

type Engine struct {
	reader   Reader
	signals  SignalStore
	deploys  DeploySource
	store    Store
	cfg      Config
	metrics  *metrics.Metrics
	log      *slog.Logger
	notifier Notifier
	now      func() time.Time

	mu     sync.RWMutex
	active map[string]Incident

	// pendingTraffic counts consecutive anomalous ticks per "service|direction"
	// for the sustained-anomaly gate. Touched only on the (serial) tick path.
	pendingTraffic map[string]int
}

// SetNotifier attaches an outbound notifier. Optional; nil leaves notification
// off. Called once at wiring time before ticks start.
func (e *Engine) SetNotifier(n Notifier) { e.notifier = n }

func NewEngine(reader Reader, signalStore SignalStore, deploys DeploySource, store Store, cfg Config, m *metrics.Metrics, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		reader:         reader,
		signals:        signalStore,
		deploys:        deploys,
		store:          store,
		cfg:            cfg.withDefaults(),
		metrics:        m,
		log:            log,
		now:            time.Now,
		active:         map[string]Incident{},
		pendingTraffic: map[string]int{},
	}
}

func (e *Engine) Bootstrap(ctx context.Context) error {
	rows, err := e.store.ListActive(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = map[string]Incident{}
	for _, inc := range rows {
		e.active[inc.IncidentID] = inc
	}
	if e.metrics != nil {
		e.metrics.IncidentActive.Set(float64(len(rows)))
	}
	return nil
}

func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.TickInterval)
	defer ticker.Stop()
	e.log.Info("incident engine started", "interval", e.cfg.TickInterval, "window", e.cfg.Window)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Tick(ctx); err != nil {
				e.log.Warn("incident tick failed", "err", err)
			}
		}
	}
}

func (e *Engine) Tick(ctx context.Context) error {
	start := time.Now()
	if e.metrics != nil {
		defer func() { e.metrics.IncidentTickLatency.Observe(time.Since(start).Seconds()) }()
	}
	now := e.now().UTC()
	rows, err := e.derive(ctx, now, e.SnapshotActive(), e.reader)
	if err != nil {
		return err
	}
	return e.ApplyLive(ctx, rows)
}

type derivedRow struct {
	Incident Incident
	Existed  bool
}

// derive computes incident rows for the cycle from the seed + reader without
// touching e.active or the store. Used by both live Tick and startup Rebuild.
func (e *Engine) derive(ctx context.Context, now time.Time, seed map[string]Incident, reader Reader) ([]derivedRow, error) {
	currentStart := now.Add(-e.cfg.Window)
	statuses := failedStatuses()
	current := reader.Errors(SearchFilter{Since: currentStart, Until: now, Statuses: statuses}, 200)
	// Baseline = per-family median of the 3 prior windows (newest first); a
	// family absent from a window counts 0. One anomalous prior window can
	// neither suppress a spike nor fabricate lift (docs/internals.md).
	baselineByFamily := map[string][3]int{}
	for i := 0; i < 3; i++ {
		until := now.Add(-time.Duration(i+1) * e.cfg.Window)
		since := now.Add(-time.Duration(i+2) * e.cfg.Window)
		res := reader.Errors(SearchFilter{Since: since, Until: until, Statuses: statuses}, 200)
		for _, row := range res.Rows {
			key := familyKey(row.ErrorFamily)
			counts := baselineByFamily[key]
			counts[i] = row.Count
			baselineByFamily[key] = counts
		}
	}

	seen := map[string]struct{}{}
	out := make([]derivedRow, 0, len(current.Rows))
	for _, row := range current.Rows {
		if row.Count < e.cfg.MinCount {
			continue
		}
		if e.cfg.MinRate > 0 && float64(row.Count) < e.cfg.MinRate*e.cfg.Window.Minutes() {
			continue
		}
		baselineCount := median3(baselineByFamily[familyKey(row.ErrorFamily)])
		lift := computeLift(row.Count, baselineCount)
		if baselineCount > 0 && lift < e.cfg.MinLift {
			continue
		}
		inc, existed, err := e.buildIncidentFromSeed(ctx, seed, reader, row, baselineCount, lift, currentStart, now)
		if err != nil {
			return nil, err
		}
		seen[inc.IncidentID] = struct{}{}
		out = append(out, derivedRow{Incident: inc, Existed: existed})
	}
	// Volume + latency detectors share ONE per-window stats snapshot (count +
	// optional percentile), so enabling both does not double the index scans.
	if e.cfg.TrafficAnomaly.Enabled || e.cfg.LatencyAnomaly.Enabled {
		pct := 0
		if e.cfg.LatencyAnomaly.Enabled {
			pct = e.cfg.LatencyAnomaly.Percentile // 0 (traffic-only) skips the latency sort
		}
		curStats := reader.ServiceStats(SearchFilter{Since: currentStart, Until: now}, pct, serviceStatsReadLimit)
		var baseStats [3][]ServiceStatsRow
		for i := 0; i < 3; i++ {
			until := now.Add(-time.Duration(i+1) * e.cfg.Window)
			since := now.Add(-time.Duration(i+2) * e.cfg.Window)
			baseStats[i] = reader.ServiceStats(SearchFilter{Since: since, Until: until}, pct, serviceStatsReadLimit)
		}
		if e.cfg.TrafficAnomaly.Enabled {
			trafficRows, err := e.deriveTrafficAnomalies(ctx, now, seed, reader, curStats, baseStats, seen)
			if err != nil {
				return nil, err
			}
			out = append(out, trafficRows...)
		}
		if e.cfg.LatencyAnomaly.Enabled {
			latencyRows, err := e.deriveLatencyAnomalies(ctx, now, seed, reader, curStats, baseStats, seen)
			if err != nil {
				return nil, err
			}
			out = append(out, latencyRows...)
		}
	}
	out = append(out, e.deriveMissing(seed, seen, now)...)
	return out, nil
}

// trafficDir is the direction of a volume anomaly.
type trafficDir string

const (
	trafficDrop  trafficDir = "drop"
	trafficSurge trafficDir = "surge"

	// trafficStep is the synthetic step for traffic-anomaly incidents; the
	// synthetic error codes key the incident family. Single source.
	trafficStep           = "<traffic>"
	errorCodeTrafficDrop  = "TRAFFIC_DROP"
	errorCodeTrafficSurge = "TRAFFIC_SURGE"

	// latencyStep / code key latency-anomaly incidents (synthetic family).
	latencyStep           = "<latency>"
	errorCodeLatencySpike = "LATENCY_SPIKE"

	serviceStatsReadLimit = 500
)

// deriveTrafficAnomalies detects per-service request-volume drop/surge against
// the median of the 3 prior windows (the same baseline machinery as errors),
// gated by a min-volume floor and a sustained-anomaly count. Operates on
// pre-fetched per-window stats (shared with the latency detector). Opened
// incidents are added to seen so the shared recover/resolve path manages them.
func (e *Engine) deriveTrafficAnomalies(ctx context.Context, now time.Time, seed map[string]Incident, reader Reader, curStats []ServiceStatsRow, baseStats [3][]ServiceStatsRow, seen map[string]struct{}) ([]derivedRow, error) {
	cfg := e.cfg.TrafficAnomaly

	current := map[string]int{}
	for _, row := range curStats {
		current[row.Service] = row.Count
	}
	baselineByService := map[string][3]int{}
	for i := 0; i < 3; i++ {
		for _, row := range baseStats[i] {
			b := baselineByService[row.Service]
			b[i] = row.Count
			baselineByService[row.Service] = b
		}
	}

	// Deterministic evaluation order over the union of services.
	svcSet := map[string]struct{}{}
	for s := range current {
		svcSet[s] = struct{}{}
	}
	for s := range baselineByService {
		svcSet[s] = struct{}{}
	}
	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)

	out := make([]derivedRow, 0)
	for _, svc := range services {
		baseline := median3(baselineByService[svc])
		dropKey, surgeKey := svc+"|drop", svc+"|surge"
		// Floor: only services with real prior traffic are eligible (also avoids
		// "drop from nothing" and divide-by-zero).
		if baseline < cfg.MinVolume {
			delete(e.pendingTraffic, dropKey)
			delete(e.pendingTraffic, surgeKey)
			continue
		}
		cur := current[svc]
		dir := trafficDir("")
		switch {
		case float64(cur) <= cfg.DropFactor*float64(baseline):
			dir = trafficDrop
		case cfg.SurgeFactor > 0 && float64(cur) >= cfg.SurgeFactor*float64(baseline):
			dir = trafficSurge
		}
		if dir == "" {
			delete(e.pendingTraffic, dropKey)
			delete(e.pendingTraffic, surgeKey)
			continue
		}
		key := svc + "|" + string(dir)
		other := dropKey
		if dir == trafficDrop {
			other = surgeKey
		}
		delete(e.pendingTraffic, other)
		e.pendingTraffic[key]++
		if e.pendingTraffic[key] < cfg.SustainedTicks {
			continue
		}
		env := e.trafficEnv(reader, svc, now)
		inc, existed, err := e.buildTrafficIncident(ctx, seed, svc, env, dir, cur, baseline, now)
		if err != nil {
			return nil, err
		}
		seen[inc.IncidentID] = struct{}{}
		out = append(out, derivedRow{Incident: inc, Existed: existed})
	}
	return out, nil
}

// trafficEnv infers the environment for a service from a recent sample event
// (looks back several windows so a fully-dropped service still resolves an env).
func (e *Engine) trafficEnv(reader Reader, service string, now time.Time) string {
	evs := reader.SearchEvents(SearchFilter{
		Service: service,
		Since:   now.Add(-4 * e.cfg.Window),
		Until:   now,
	}, 1)
	return firstEventEnv(evs)
}

// deriveLatencyAnomalies detects per-service tail-latency spikes: the current
// window's pX vs the median of the qualifying prior-3-window pX values. A service
// is eligible only when the current window and >=2 baseline windows each clear
// MinRequests (a percentile over too few samples is meaningless). Sustained-gated
// and added to seen so the shared recover/resolve path manages lifecycle.
func (e *Engine) deriveLatencyAnomalies(ctx context.Context, now time.Time, seed map[string]Incident, reader Reader, curStats []ServiceStatsRow, baseStats [3][]ServiceStatsRow, seen map[string]struct{}) ([]derivedRow, error) {
	cfg := e.cfg.LatencyAnomaly

	type lat struct {
		ms      int64
		samples int
	}
	current := map[string]lat{}
	for _, row := range curStats {
		current[row.Service] = lat{ms: row.LatencyMS, samples: row.Count}
	}
	// Per-service baseline pX values from the 3 prior windows, with sample counts.
	baseline := map[string][]lat{}
	for i := 0; i < 3; i++ {
		for _, row := range baseStats[i] {
			baseline[row.Service] = append(baseline[row.Service], lat{ms: row.LatencyMS, samples: row.Count})
		}
	}

	svcSet := map[string]struct{}{}
	for s := range current {
		svcSet[s] = struct{}{}
	}
	for s := range baseline {
		svcSet[s] = struct{}{}
	}
	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)

	out := make([]derivedRow, 0)
	for _, svc := range services {
		key := svc + "|latency"
		cur := current[svc]
		// Current window must have enough samples for a meaningful percentile.
		if cur.samples < cfg.MinRequests {
			delete(e.pendingTraffic, key)
			continue
		}
		// Baseline = median of windows that themselves cleared MinRequests; need >=2.
		qualifying := make([]int, 0, 3)
		for _, b := range baseline[svc] {
			if b.samples >= cfg.MinRequests {
				qualifying = append(qualifying, int(b.ms))
			}
		}
		if len(qualifying) < 2 {
			delete(e.pendingTraffic, key)
			continue
		}
		base := medianInts(qualifying)
		anomalous := base > 0 &&
			float64(cur.ms) >= cfg.Factor*float64(base) &&
			(cfg.MinMS == 0 || cur.ms >= cfg.MinMS)
		if !anomalous {
			delete(e.pendingTraffic, key)
			continue
		}
		e.pendingTraffic[key]++
		if e.pendingTraffic[key] < cfg.SustainedTicks {
			continue
		}
		env := e.trafficEnv(reader, svc, now)
		inc, existed, err := e.buildLatencyIncident(ctx, seed, svc, env, cfg.Percentile, cur.ms, int64(base), now)
		if err != nil {
			return nil, err
		}
		seen[inc.IncidentID] = struct{}{}
		out = append(out, derivedRow{Incident: inc, Existed: existed})
	}
	return out, nil
}

// medianInts returns the median of vs (sorted; lower-middle for even length, so
// the result is deterministic). vs must be non-empty.
func medianInts(vs []int) int {
	s := append([]int(nil), vs...)
	sort.Ints(s)
	return s[(len(s)-1)/2]
}

// deriveMissing emits transitions for seed rows absent from the current cycle:
// active → recovering, and recovering → resolved once LastSeenAt is older
// than ResolveAfter.
func (e *Engine) deriveMissing(seed map[string]Incident, seen map[string]struct{}, now time.Time) []derivedRow {
	out := make([]derivedRow, 0)
	for _, inc := range seed {
		if _, ok := seen[inc.IncidentID]; ok {
			continue
		}
		switch inc.Status {
		case StatusActive:
			row := cloneIncident(inc)
			row.Status = StatusRecovering
			t := now
			row.RecoveringAt = &t
			row.UpdatedAt = now
			out = append(out, derivedRow{Incident: row, Existed: true})
		case StatusRecovering:
			if now.Sub(inc.LastSeenAt) >= e.cfg.ResolveAfter {
				row := cloneIncident(inc)
				row.Status = StatusResolved
				t := now
				row.ResolvedAt = &t
				row.UpdatedAt = now
				out = append(out, derivedRow{Incident: row, Existed: true})
			}
		}
	}
	return out
}

// ApplyLive persists derived rows for a live tick: per-row Upsert, in-memory
// cache update, and per-transition metric increments.
func (e *Engine) ApplyLive(ctx context.Context, rows []derivedRow) error {
	for _, dr := range rows {
		if err := e.store.Upsert(ctx, dr.Incident); err != nil {
			return err
		}
		switch dr.Incident.Status {
		case StatusResolved:
			e.forget(dr.Incident.IncidentID)
			if e.metrics != nil {
				e.metrics.IncidentResolved.Inc()
			}
			if e.notifier != nil {
				e.notifier.IncidentResolved(dr.Incident)
			}
		case StatusRecovering:
			e.remember(dr.Incident)
			if dr.Existed {
				if e.metrics != nil {
					e.metrics.IncidentRecovered.Inc()
				}
			}
		default:
			e.remember(dr.Incident)
			if e.metrics != nil {
				if dr.Existed {
					e.metrics.IncidentUpdated.Inc()
				} else {
					e.metrics.IncidentOpened.Inc()
				}
			}
			// Notify exactly once, on the open transition (not on per-tick updates).
			if !dr.Existed && e.notifier != nil {
				e.notifier.IncidentOpened(dr.Incident)
			}
		}
	}
	if e.metrics != nil {
		e.metrics.IncidentActive.Set(float64(e.activeCount()))
	}
	return nil
}

// ApplyRebuild atomically replaces non-resolved store rows with the derived
// set, then reloads the in-memory cache from the store. ApplyRebuild owns
// cache reload; do NOT call Bootstrap after it.
func (e *Engine) ApplyRebuild(ctx context.Context, rows []derivedRow) error {
	incs := make([]Incident, 0, len(rows))
	for _, dr := range rows {
		incs = append(incs, dr.Incident)
	}
	if err := e.store.ReplaceNonResolved(ctx, incs); err != nil {
		return err
	}
	active, err := e.store.ListActive(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.active = make(map[string]Incident, len(active))
	for _, inc := range active {
		e.active[inc.IncidentID] = cloneIncident(inc)
	}
	e.mu.Unlock()
	if e.metrics != nil {
		e.metrics.IncidentActive.Set(float64(len(active)))
	}
	return nil
}

// SnapshotActive returns a deep clone of the in-memory active map.
func (e *Engine) SnapshotActive() map[string]Incident {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]Incident, len(e.active))
	for id, inc := range e.active {
		out[id] = cloneIncident(inc)
	}
	return out
}

func (e *Engine) Active(ctx context.Context) ([]Incident, error) {
	rows, err := e.store.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	sortIncidents(rows)
	return rows, nil
}

func (e *Engine) Get(ctx context.Context, id string) (Incident, error) {
	return e.store.Get(ctx, id)
}

func (e *Engine) TopActive(ctx context.Context) (*Incident, error) {
	rows, err := e.Active(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// safeBlastRadius wraps reader.BlastRadius with a panic recover so a reader
// fault never propagates into the tick. ok=false means the reader call
// faulted; callers should treat the returned response as zero-value and
// record CaptureStatus=missing for downstream evidence.
func safeBlastRadius(r Reader, f SearchFilter, k apiv2.BlastKey) (out apiv2.BlastRadiusResponse, ok bool) {
	if r == nil {
		return apiv2.BlastRadiusResponse{}, false
	}
	ok = true
	defer func() {
		if rec := recover(); rec != nil {
			out, ok = apiv2.BlastRadiusResponse{}, false
		}
	}()
	out = r.BlastRadius(f, k)
	return out, ok
}

func safeTraceStory(r Reader, traceID string) (resp apiv2.StoryResponse, ok bool) {
	if r == nil {
		return apiv2.StoryResponse{}, false
	}
	defer func() {
		if rec := recover(); rec != nil {
			resp, ok = apiv2.StoryResponse{}, false
		}
	}()
	return r.TraceStoryByTraceID(traceID)
}

func safeTraceEvents(r Reader, traceID string) (events []*eventv2.Event, ok bool) {
	if r == nil {
		return nil, false
	}
	defer func() {
		if rec := recover(); rec != nil {
			events, ok = nil, false
		}
	}()
	return r.TraceEvents(traceID)
}

func filterEventsByTrace(events []*eventv2.Event, traceID string) []*eventv2.Event {
	out := make([]*eventv2.Event, 0, 1)
	for _, ev := range events {
		if ev != nil && ev.TraceID == traceID {
			out = append(out, ev)
		}
	}
	return out
}

func (e *Engine) buildIncidentFromSeed(ctx context.Context, seed map[string]Incident, reader Reader, row apiv2.ErrorRow, baselineCount int, lift float64, since, now time.Time) (Incident, bool, error) {
	events := sampleEventsFromReader(reader, row.ErrorFamily, since, now, 200)
	startedAt := earliestEventTime(events, now)
	env := firstEventEnv(events)
	if existing, ok := findByFamilyIn(seed, env, row.ErrorFamily); ok {
		startedAt = existing.StartedAt
	}
	id := StableID(env, row.ErrorFamily, startedAt)
	existing, hadExisting := getCachedIn(seed, id)
	if !hadExisting {
		if prior, ok := findByFamilyIn(seed, env, row.ErrorFamily); ok {
			existing = prior
			id = prior.IncidentID
			hadExisting = true
		}
	}
	blast, blastOK := safeBlastRadius(reader,
		SearchFilter{Since: since, Until: now},
		apiv2.BlastKey{Service: row.ErrorFamily.Service, Step: row.ErrorFamily.Step, ErrorCode: row.ErrorFamily.ErrorCode},
	)
	signalSince := now.Add(-e.cfg.DeployCorrelationWindow)
	if alertSince := startedAt.Add(-e.cfg.DeployCorrelationWindow); alertSince.Before(signalSince) {
		signalSince = alertSince
	}
	sigs, err := e.querySignals(ctx, env, signalSince, now)
	if err != nil && !errors.Is(err, signals.ErrUnavailable) {
		return Incident{}, false, err
	}
	deploys, err := e.queryDeploys(ctx, row.ErrorFamily.Service, now.Add(-e.cfg.DeployCorrelationWindow), now)
	if err != nil {
		return Incident{}, false, err
	}
	inc := Incident{
		IncidentID:       id,
		Env:              env,
		Service:          row.ErrorFamily.Service,
		ErrorFamily:      row.ErrorFamily,
		Status:           StatusActive,
		Severity:         severity(row.Count, blast.AffectedServices, lift),
		StartedAt:        startedAt,
		UpdatedAt:        now,
		LastSeenAt:       now,
		AffectedRequests: blast.AffectedRequests,
		AffectedUsers:    cloneInt(row.AffectedUsers),
		AffectedServices: blast.AffectedServices,
		TopServices:      append([]string(nil), blast.TopServices...),
		SampleTraces:     stableSamples(existing.SampleTraces, events, e.cfg.SampleLimit),
		Lift:             lift,
		BaselineCount:    baselineCount,
		CurrentCount:     row.Count,
	}
	if hadExisting {
		inc.StartedAt = existing.StartedAt
		inc.RecoveringAt = nil
	}
	if reader != nil && inc.Status != StatusResolved {
		blastStatus := CaptureOK
		if !blastOK {
			blastStatus = CaptureMissing
		}
		inc.Blast = updateBlastSnapshot(existing.Blast, newBlastEvidence(blast, now, blastStatus))

		var story *apiv2.StoryResponse
		var firstSeenAt *time.Time
		var sampleTraceID string
		if blastOK && len(blast.SampleTraces) > 0 {
			sampleTraceID = blast.SampleTraces[0]
			if s, ok := safeTraceStory(reader, sampleTraceID); ok {
				story = &s
			}
			// Prefer events already loaded for this family scan; fall back to a
			// dedicated TraceEvents read only if the sample trace had no
			// anchor-matching event in that scan.
			traceEvts := filterEventsByTrace(events, sampleTraceID)
			if len(traceEvts) == 0 {
				if evts, ok := safeTraceEvents(reader, sampleTraceID); ok {
					traceEvts = evts
				}
			}
			if ts, ok2 := pickAnchorTsStart(traceEvts, inc.ErrorFamily); ok2 {
				firstSeenAt = &ts
			}
		}
		inc.Propagation = updatePropagationSnapshot(existing.Propagation, newPropagationEvidence(story, sampleTraceID, firstSeenAt, now))
	}
	if inc.Status != StatusResolved {
		inc.Alerts = updateAlertSnapshot(existing.Alerts, captureAlertEvidenceFromSignals(sigs, inc, now, e.cfg.DeployCorrelationWindow))
		inc.Runtime = updateRuntimeSnapshot(existing.Runtime, captureRuntimeEvidence(sigs, inc, now, e.cfg.DeployCorrelationWindow))
	}
	class := Classify(ClassificationInput{Incident: inc, Events: events, Signals: sigs, Deployments: deploys, Now: now})
	inc.Cause = class.Cause
	inc.Confidence = class.Confidence
	inc.Evidence = class.Evidence
	inc.NextChecks = class.NextChecks
	inc.InstrumentationWarnings = class.InstrumentationWarnings
	// Sticky suspect deploy: carry the prior correlation forward and refresh it
	// when this tick matched one. Once set it is never cleared, so the triage
	// Suspect Change survives re-classification and evidence-cap churn.
	inc.SuspectDeployID = existing.SuspectDeployID
	if class.SuspectDeployID != "" {
		inc.SuspectDeployID = class.SuspectDeployID
	}
	if e.metrics != nil {
		e.observeClassification(inc.Cause, inc.Confidence)
	}
	return inc, hadExisting, nil
}

// buildTrafficIncident projects a volume anomaly into an Incident. It reuses the
// signal/deploy correlation + classifier (so a drop right after a deploy is
// classified cause=deploy and suspect_change surfaces it) but skips the
// error-trace/blast/propagation path, which has no data for a volume anomaly.
func (e *Engine) buildTrafficIncident(ctx context.Context, seed map[string]Incident, service, env string, dir trafficDir, current, baseline int, now time.Time) (Incident, bool, error) {
	code := errorCodeTrafficDrop
	if dir == trafficSurge {
		code = errorCodeTrafficSurge
	}
	family := apiv2.ErrorFamily{Service: service, Step: trafficStep, ErrorCode: code}
	return e.buildAnomalyIncident(ctx, seed, env, family, current, baseline, trafficLift(current, baseline, dir),
		trafficEvidence(service, dir, current, baseline, now), now)
}

// buildAnomalyIncident projects a volume/latency anomaly into an Incident. It
// reuses signal/deploy correlation + the classifier (so an anomaly right after a
// deploy classifies cause=deploy with suspect_change) and skips the error-trace
// path, which has no data for a synthetic-family anomaly. leadEvidence is the
// cited measurement row; severity scales with the larger of current/baseline.
func (e *Engine) buildAnomalyIncident(ctx context.Context, seed map[string]Incident, env string, family apiv2.ErrorFamily, current, baseline int, lift float64, leadEvidence Evidence, now time.Time) (Incident, bool, error) {
	startedAt := now
	if prior, ok := findByFamilyIn(seed, env, family); ok {
		startedAt = prior.StartedAt
	}
	id := StableID(env, family, startedAt)
	existing, hadExisting := getCachedIn(seed, id)
	if !hadExisting {
		if prior, ok := findByFamilyIn(seed, env, family); ok {
			existing = prior
			id = prior.IncidentID
			hadExisting = true
		}
	}

	sigs, err := e.querySignals(ctx, env, now.Add(-e.cfg.DeployCorrelationWindow), now)
	if err != nil && !errors.Is(err, signals.ErrUnavailable) {
		return Incident{}, false, err
	}
	deploys, err := e.queryDeploys(ctx, family.Service, now.Add(-e.cfg.DeployCorrelationWindow), now)
	if err != nil {
		return Incident{}, false, err
	}

	inc := Incident{
		IncidentID:    id,
		Env:           env,
		Service:       family.Service,
		ErrorFamily:   family,
		Status:        StatusActive,
		Severity:      severity(maxInt(current, baseline), 1, lift),
		StartedAt:     startedAt,
		UpdatedAt:     now,
		LastSeenAt:    now,
		Lift:          lift,
		BaselineCount: baseline,
		CurrentCount:  current,
	}
	if hadExisting {
		inc.StartedAt = existing.StartedAt
		inc.RecoveringAt = nil
	}
	inc.Alerts = updateAlertSnapshot(existing.Alerts, captureAlertEvidenceFromSignals(sigs, inc, now, e.cfg.DeployCorrelationWindow))
	inc.Runtime = updateRuntimeSnapshot(existing.Runtime, captureRuntimeEvidence(sigs, inc, now, e.cfg.DeployCorrelationWindow))

	class := Classify(ClassificationInput{Incident: inc, Events: nil, Signals: sigs, Deployments: deploys, Now: now})
	inc.Cause = class.Cause
	inc.Confidence = class.Confidence
	// Lead with the cited measurement row, then the classifier's correlation
	// evidence (deploy/signal/runtime).
	inc.Evidence = append([]Evidence{leadEvidence}, class.Evidence...)
	inc.NextChecks = class.NextChecks
	inc.InstrumentationWarnings = class.InstrumentationWarnings
	inc.SuspectDeployID = existing.SuspectDeployID
	if class.SuspectDeployID != "" {
		inc.SuspectDeployID = class.SuspectDeployID
	}
	if e.metrics != nil {
		e.observeClassification(inc.Cause, inc.Confidence)
	}
	return inc, hadExisting, nil
}

// trafficLift quantifies how far volume moved: for a drop, how many× below
// baseline (baseline/current); for a surge, how many× above (current/baseline).
func trafficLift(current, baseline int, dir trafficDir) float64 {
	if dir == trafficSurge {
		return float64(current) / math.Max(float64(baseline), 1)
	}
	return float64(baseline) / math.Max(float64(current), 1)
}

func trafficEvidence(service string, dir trafficDir, current, baseline int, now time.Time) Evidence {
	verb := "dropped"
	if dir == trafficSurge {
		verb = "surged"
	}
	return Evidence{
		Kind:       EvidenceTraffic,
		Title:      "Request volume " + verb,
		Detail:     service,
		Service:    service,
		OccurredAt: now,
		Fields: map[string]any{
			"direction": string(dir),
			"current":   current,
			"baseline":  baseline,
		},
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildLatencyIncident projects a tail-latency spike into an Incident via the
// shared anomaly builder. currentMS/baselineMS are the tracked percentile values.
func (e *Engine) buildLatencyIncident(ctx context.Context, seed map[string]Incident, service, env string, percentile int, currentMS, baselineMS int64, now time.Time) (Incident, bool, error) {
	family := apiv2.ErrorFamily{Service: service, Step: latencyStep, ErrorCode: errorCodeLatencySpike}
	lift := float64(currentMS) / math.Max(float64(baselineMS), 1)
	return e.buildAnomalyIncident(ctx, seed, env, family, int(currentMS), int(baselineMS), lift,
		latencyEvidence(service, percentile, currentMS, baselineMS, now), now)
}

func latencyEvidence(service string, percentile int, currentMS, baselineMS int64, now time.Time) Evidence {
	return Evidence{
		Kind:       EvidenceLatency,
		Title:      "Tail latency spiked",
		Detail:     service,
		Service:    service,
		OccurredAt: now,
		Fields: map[string]any{
			"percentile":  percentile,
			"current_ms":  currentMS,
			"baseline_ms": baselineMS,
		},
	}
}

func sampleEventsFromReader(reader Reader, f apiv2.ErrorFamily, since, until time.Time, limit int) []*eventv2.Event {
	events := reader.SearchEvents(SearchFilter{
		Service:   f.Service,
		ErrorCode: f.ErrorCode,
		Since:     since,
		Until:     until,
		Statuses:  failedStatuses(),
	}, limit)
	out := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		if ev != nil && ev.Anchor != nil && ev.Anchor.Step == f.Step {
			out = append(out, ev)
		}
	}
	return out
}

func getCachedIn(seed map[string]Incident, id string) (Incident, bool) {
	inc, ok := seed[id]
	return cloneIncident(inc), ok
}

func findByFamilyIn(seed map[string]Incident, env string, family apiv2.ErrorFamily) (Incident, bool) {
	for _, inc := range seed {
		if inc.Env == env && inc.ErrorFamily == family && inc.Status != StatusResolved {
			return cloneIncident(inc), true
		}
	}
	return Incident{}, false
}

func (e *Engine) querySignals(ctx context.Context, env string, since, until time.Time) ([]signals.Signal, error) {
	if e.signals == nil {
		return nil, nil
	}
	return e.signals.Query(ctx, signals.Filter{Env: env, Since: since, Until: until, Limit: 200})
}

func (e *Engine) queryDeploys(ctx context.Context, service string, since, until time.Time) ([]Deployment, error) {
	if e.deploys == nil {
		return nil, nil
	}
	return e.deploys.DeploymentsInWindow(ctx, since, until, service)
}

func (e *Engine) remember(inc Incident) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[inc.IncidentID] = cloneIncident(inc)
}

func (e *Engine) forget(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, id)
}

func (e *Engine) activeCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.active)
}

func (e *Engine) observeClassification(cause Cause, confidence Confidence) {
	if e.metrics == nil {
		return
	}
	e.metrics.IncidentClassifications.With(prometheus.Labels{
		"cause":      string(cause),
		"confidence": string(confidence),
	}).Inc()
}

func failedStatuses() map[eventv2.Status]struct{} {
	return map[eventv2.Status]struct{}{
		eventv2.StatusError:   {},
		eventv2.StatusTimeout: {},
		eventv2.StatusPartial: {},
		eventv2.StatusAborted: {},
	}
}

func computeLift(current, baseline int) float64 {
	if baseline <= 0 {
		return float64(current)
	}
	return float64(current) / float64(baseline)
}

func median3(c [3]int) int {
	s := []int{c[0], c[1], c[2]}
	sort.Ints(s)
	return s[1]
}

func severity(count, services int, lift float64) int {
	score := 1 + count/5 + services
	if lift >= 10 {
		score += 3
	} else if lift >= 3 {
		score += 2
	}
	return int(math.Min(10, float64(score)))
}

func familyKey(f apiv2.ErrorFamily) string {
	return f.Service + "\x00" + f.Step + "\x00" + f.ErrorCode
}

func earliestEventTime(events []*eventv2.Event, fallback time.Time) time.Time {
	out := fallback
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if out.IsZero() || ev.TsStart.Before(out) {
			out = ev.TsStart
		}
	}
	return out.UTC()
}

func firstEventEnv(events []*eventv2.Event) string {
	for _, ev := range events {
		if ev != nil && ev.Env != "" {
			return ev.Env
		}
	}
	return "unknown"
}

func stableSamples(existing []string, events []*eventv2.Event, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := append([]string(nil), existing...)
	seen := map[string]struct{}{}
	for _, traceID := range out {
		seen[traceID] = struct{}{}
	}
	if len(out) == 0 {
		ascending := append([]*eventv2.Event(nil), events...)
		sort.SliceStable(ascending, func(i, j int) bool {
			if !ascending[i].TsStart.Equal(ascending[j].TsStart) {
				return ascending[i].TsStart.Before(ascending[j].TsStart)
			}
			return ascending[i].TraceID < ascending[j].TraceID
		})
		for _, ev := range ascending {
			if ev != nil && ev.TraceID != "" {
				out = append(out, ev.TraceID)
				seen[ev.TraceID] = struct{}{}
				break
			}
		}
	}
	recent := append([]*eventv2.Event(nil), events...)
	sort.SliceStable(recent, func(i, j int) bool {
		if !recent[i].TsStart.Equal(recent[j].TsStart) {
			return recent[i].TsStart.After(recent[j].TsStart)
		}
		return recent[i].TraceID < recent[j].TraceID
	})
	for _, ev := range recent {
		if ev == nil || ev.TraceID == "" {
			continue
		}
		if _, ok := seen[ev.TraceID]; ok {
			continue
		}
		out = append(out, ev.TraceID)
		seen[ev.TraceID] = struct{}{}
		if len(out) == limit {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func cloneInt(in *int) *int {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}
