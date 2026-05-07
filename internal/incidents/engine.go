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
	ResolveAfter            time.Duration
	DeployCorrelationWindow time.Duration
	SampleLimit             int
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

type Engine struct {
	reader  Reader
	signals SignalStore
	deploys DeploySource
	store   Store
	cfg     Config
	metrics *metrics.Metrics
	log     *slog.Logger
	now     func() time.Time

	mu     sync.RWMutex
	active map[string]Incident
}

func NewEngine(reader Reader, signalStore SignalStore, deploys DeploySource, store Store, cfg Config, m *metrics.Metrics, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		reader:  reader,
		signals: signalStore,
		deploys: deploys,
		store:   store,
		cfg:     cfg.withDefaults(),
		metrics: m,
		log:     log,
		now:     time.Now,
		active:  map[string]Incident{},
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

// derivedRow carries the derivation output plus whether the row was already
// in the seed (used by ApplyLive to distinguish Opened vs Updated metrics).
type derivedRow struct {
	Incident Incident
	Existed  bool
}

// derive computes the full set of incident rows for the cycle from the seed +
// reader without touching e.active or the store. Used by both live Tick and
// startup Rebuild.
func (e *Engine) derive(ctx context.Context, now time.Time, seed map[string]Incident, reader Reader) ([]derivedRow, error) {
	currentStart := now.Add(-e.cfg.Window)
	baselineStart := now.Add(-2 * e.cfg.Window)
	statuses := failedStatuses()
	current := reader.Errors(SearchFilter{Since: currentStart, Until: now, Statuses: statuses}, 200)
	baseline := reader.Errors(SearchFilter{Since: baselineStart, Until: currentStart, Statuses: statuses}, 200)
	baselineByFamily := map[string]int{}
	for _, row := range baseline.Rows {
		baselineByFamily[familyKey(row.ErrorFamily)] = row.Count
	}

	seen := map[string]struct{}{}
	out := make([]derivedRow, 0, len(current.Rows))
	for _, row := range current.Rows {
		if row.Count < e.cfg.MinCount {
			continue
		}
		baselineCount := baselineByFamily[familyKey(row.ErrorFamily)]
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
	out = append(out, e.deriveMissing(seed, seen, now)...)
	return out, nil
}

// deriveMissing emits transitions for seed rows absent from the current cycle:
// active → recovering, and recovering → resolved once LastSeenAt is older
// than ResolveAfter. Mirrors the previous transitionMissing semantics.
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
// cache update, and per-transition metric increments matching pre-refactor
// Tick behavior.
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
		}
	}
	if e.metrics != nil {
		e.metrics.IncidentActive.Set(float64(e.activeCount()))
	}
	return nil
}

// ApplyRebuild atomically replaces non-resolved store rows with the derived
// set, then reloads the in-memory cache from the store. ApplyRebuild owns
// cache reload; do NOT call Bootstrap after it. Per-row Opened/Updated/
// Recovered/Resolved counters are intentionally not incremented here —
// rebuild metrics live in main.go.
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

func (e *Engine) buildIncident(ctx context.Context, row apiv2.ErrorRow, baselineCount int, lift float64, since, now time.Time) (Incident, error) {
	events := e.sampleEvents(row.ErrorFamily, since, now, 200)
	startedAt := earliestEventTime(events, now)
	env := firstEventEnv(events)
	if existing, ok := e.findByFamily(env, row.ErrorFamily); ok {
		startedAt = existing.StartedAt
	}
	id := StableID(env, row.ErrorFamily, startedAt)
	existing, hadExisting := e.getCached(id)
	if !hadExisting {
		if prior, ok := e.findByFamily(env, row.ErrorFamily); ok {
			existing = prior
			id = prior.IncidentID
			hadExisting = true
		}
	}
	blast := e.reader.BlastRadius(
		SearchFilter{Since: since, Until: now},
		apiv2.BlastKey{Service: row.ErrorFamily.Service, Step: row.ErrorFamily.Step, ErrorCode: row.ErrorFamily.ErrorCode},
	)
	sigs, err := e.querySignals(ctx, env, now.Add(-e.cfg.DeployCorrelationWindow), now)
	if err != nil && !errors.Is(err, signals.ErrUnavailable) {
		return Incident{}, err
	}
	deploys, err := e.queryDeploys(ctx, row.ErrorFamily.Service, now.Add(-e.cfg.DeployCorrelationWindow), now)
	if err != nil {
		return Incident{}, err
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
	class := Classify(ClassificationInput{Incident: inc, Events: events, Signals: sigs, Deployments: deploys, Now: now})
	inc.Cause = class.Cause
	inc.Confidence = class.Confidence
	inc.Evidence = class.Evidence
	inc.NextChecks = class.NextChecks
	inc.InstrumentationWarnings = class.InstrumentationWarnings
	e.observeClassification(inc.Cause, inc.Confidence)
	if e.metrics != nil {
		if hadExisting {
			e.metrics.IncidentUpdated.Inc()
		} else {
			e.metrics.IncidentOpened.Inc()
		}
	}
	return inc, nil
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
	blast := reader.BlastRadius(
		SearchFilter{Since: since, Until: now},
		apiv2.BlastKey{Service: row.ErrorFamily.Service, Step: row.ErrorFamily.Step, ErrorCode: row.ErrorFamily.ErrorCode},
	)
	sigs, err := e.querySignals(ctx, env, now.Add(-e.cfg.DeployCorrelationWindow), now)
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
	class := Classify(ClassificationInput{Incident: inc, Events: events, Signals: sigs, Deployments: deploys, Now: now})
	inc.Cause = class.Cause
	inc.Confidence = class.Confidence
	inc.Evidence = class.Evidence
	inc.NextChecks = class.NextChecks
	inc.InstrumentationWarnings = class.InstrumentationWarnings
	if e.metrics != nil {
		e.observeClassification(inc.Cause, inc.Confidence)
	}
	return inc, hadExisting, nil
}

func (e *Engine) transitionMissing(ctx context.Context, seen map[string]struct{}, now time.Time) error {
	e.mu.RLock()
	rows := make([]Incident, 0, len(e.active))
	for _, inc := range e.active {
		rows = append(rows, cloneIncident(inc))
	}
	e.mu.RUnlock()
	for _, inc := range rows {
		if _, ok := seen[inc.IncidentID]; ok {
			continue
		}
		switch inc.Status {
		case StatusActive:
			inc.Status = StatusRecovering
			t := now
			inc.RecoveringAt = &t
			inc.UpdatedAt = now
			if err := e.store.Upsert(ctx, inc); err != nil {
				return err
			}
			e.remember(inc)
			if e.metrics != nil {
				e.metrics.IncidentRecovered.Inc()
			}
		case StatusRecovering:
			if now.Sub(inc.LastSeenAt) >= e.cfg.ResolveAfter {
				inc.Status = StatusResolved
				t := now
				inc.ResolvedAt = &t
				inc.UpdatedAt = now
				if err := e.store.Upsert(ctx, inc); err != nil {
					return err
				}
				e.forget(inc.IncidentID)
				if e.metrics != nil {
					e.metrics.IncidentResolved.Inc()
				}
			}
		}
	}
	return nil
}

func (e *Engine) sampleEvents(f apiv2.ErrorFamily, since, until time.Time, limit int) []*eventv2.Event {
	return sampleEventsFromReader(e.reader, f, since, until, limit)
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

func (e *Engine) getCached(id string) (Incident, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	inc, ok := e.active[id]
	return cloneIncident(inc), ok
}

func (e *Engine) findByFamily(env string, family apiv2.ErrorFamily) (Incident, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, inc := range e.active {
		if inc.Env == env && inc.ErrorFamily == family && inc.Status != StatusResolved {
			return cloneIncident(inc), true
		}
	}
	return Incident{}, false
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
