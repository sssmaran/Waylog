package ingest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tracestory"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// unsampledCounters tracks event counts in per-minute buckets for windowed
// error rate calculations. All counters are incremented after successful WAL
// write (or after validation if no WAL), so they reflect only durably accepted events.
type unsampledCounters struct {
	mu      sync.Mutex
	buckets [120]minuteBucket // 2-hour ring buffer
}

type minuteBucket struct {
	minute int64 // unix minute (time.Now().Unix() / 60)
	total  uint64
	errors uint64
}

func (c *unsampledCounters) Inc(isError bool) {
	now := time.Now().Unix() / 60
	c.mu.Lock()
	idx := int(now % int64(len(c.buckets)))
	b := &c.buckets[idx]
	if b.minute != now {
		b.minute = now
		b.total = 0
		b.errors = 0
	}
	b.total++
	if isError {
		b.errors++
	}
	c.mu.Unlock()
}

func (c *unsampledCounters) Sum(window time.Duration) (total, errs uint64) {
	// Buffer only holds 120 minutes; for larger windows return 0 so caller
	// falls back to graph-based rate (avoids partial/misleading counts).
	if window > 120*time.Minute {
		return 0, 0
	}
	cutoff := time.Now().Add(-window).Unix() / 60
	c.mu.Lock()
	for i := range c.buckets {
		b := &c.buckets[i]
		if b.minute >= cutoff && b.total > 0 {
			total += b.total
			errs += b.errors
		}
	}
	c.mu.Unlock()
	return
}

// Server handles HTTP requests for the ingest service.
//
// Readiness semantics: /readyz gates on ingest availability, not replay
// completeness. When replay fails the server becomes ready in degraded mode —
// new events ingest correctly but historical reads (trace story, overview,
// recent traces) may return partial results until the graph is rebuilt from
// incoming traffic.
type Server struct {
	store        *store.Store
	builder      *build.Builder
	sampler      *sampler.Sampler
	metrics      *metrics.Metrics
	EventLog     *eventlog.Writer
	EventLogDir  string
	accepted       atomic.Uint64
	counters       unsampledCounters
	sampleRatePct  int
	ready          atomic.Bool
	startTime    time.Time
	maxBodyBytes int64

	// Replay state — set once during startup, read by /healthz.
	replayStatus       string // "none", "ok", "failed"
	replayError        string
	lastReplayAttempt  time.Time
	lastReplaySuccess  time.Time
}

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Store         *store.Store
	Sampler       *sampler.Sampler
	Metrics       *metrics.Metrics
	MaxBodyBytes  int64
	EventLogDir   string
	StartTime     time.Time
	SampleRatePct int // 0 means use sampler's default from env
}

// NewServer creates a new ingest server with the given configuration.
func NewServer(cfg ServerConfig) *Server {
	maxBody := cfg.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	startTime := cfg.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	s := &Server{
		store:         cfg.Store,
		builder:       build.NewBuilder(),
		sampler:       cfg.Sampler,
		metrics:       cfg.Metrics,
		maxBodyBytes:  maxBody,
		startTime:     startTime,
		EventLogDir:   cfg.EventLogDir,
		sampleRatePct: cfg.SampleRatePct,
		replayStatus:  "none",
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
	}
	if s.sampleRatePct == 0 {
		s.sampleRatePct = s.sampler.HappySampleRatePct()
	}
	return s
}

// Health handles health check requests with a JSON status summary.
// Always returns HTTP 200 — this is a status summary, not a gate.
// Use /readyz for traffic gating, /livez for liveness.
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	if s.store == nil || s.replayStatus == "failed" {
		status = "degraded"
	}

	resp := map[string]any{
		"status": status,
		"uptime": time.Since(s.startTime).Round(time.Second).String(),
		"ready":  s.ready.Load(),
	}

	if s.store != nil {
		snap := s.store.Snapshot()
		resp["store"] = map[string]any{
			"configured": true,
			"nodes":      len(snap.Nodes),
			"edges":      len(snap.Edges),
		}
	} else {
		resp["store"] = map[string]any{"configured": false}
	}

	resp["event_log"] = map[string]any{"enabled": s.EventLogDir != ""}

	replay := map[string]any{"status": s.replayStatus}
	if s.replayError != "" {
		replay["error"] = s.replayError
	}
	if !s.lastReplayAttempt.IsZero() {
		replay["last_attempt"] = s.lastReplayAttempt.Format(time.RFC3339)
	}
	if !s.lastReplaySuccess.IsZero() {
		replay["last_success"] = s.lastReplaySuccess.Format(time.RFC3339)
	}
	resp["replay"] = replay

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Livez handles liveness probe requests.
func (s *Server) Livez(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// Readyz handles readiness probe requests.
func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// SetReady marks the server as ready to accept traffic.
func (s *Server) SetReady() {
	s.ready.Store(true)
	if s.metrics != nil {
		s.metrics.Ready.Set(1)
	}
}

// SetReplayResult records the outcome of startup replay for /healthz.
// Called once during startup after replay completes or fails.
func (s *Server) SetReplayResult(err error) {
	s.lastReplayAttempt = time.Now()
	if err != nil {
		s.replayStatus = "failed"
		s.replayError = err.Error()
	} else {
		s.replayStatus = "ok"
		s.lastReplaySuccess = s.lastReplayAttempt
	}
}

// Events handles event ingestion requests.
func (s *Server) Events(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if s.metrics != nil {
		s.metrics.InFlightRequests.Inc()
		defer s.metrics.InFlightRequests.Dec()
		defer func() { s.metrics.IngestLatency.Observe(time.Since(start).Seconds()) }()
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	var ev event.WideEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&ev); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			if s.metrics != nil {
				s.metrics.EventsRejected.WithLabelValues("validation").Inc()
			}
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		slog.Warn("json decode failed", "err", err)
		if s.metrics != nil {
			s.metrics.EventsRejected.WithLabelValues("validation").Inc()
		}
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Ensure server-side timestamp sanity
	if ev.Timestamp.After(time.Now().Add(5 * time.Minute)) {
		if s.metrics != nil {
			s.metrics.EventsRejected.WithLabelValues("validation").Inc()
		}
		http.Error(w, "timestamp too far in future", http.StatusBadRequest)
		return
	}

	if err := ev.Validate(); err != nil {
		slog.Warn("event validation failed", "err", err)
		if s.metrics != nil {
			s.metrics.EventsRejected.WithLabelValues("validation").Inc()
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sampled := s.sampler.ShouldKeep(ev)

	// Write-ahead: the eventlog is the durable source of truth. If it's
	// configured and the write fails, reject the event so the client retries.
	// Nothing enters the graph without being logged first.
	if s.EventLog != nil {
		if err := s.EventLog.Write(&ev, sampled); err != nil {
			slog.Error("eventlog write failed", "err", err)
			if s.metrics != nil {
				s.metrics.EventlogFails.Inc()
			}
			http.Error(w, "event log unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// Windowed unsampled counters — incremented after successful WAL write
	// so rejected events (WAL failure → 503) are never counted.
	s.counters.Inc(!ev.Outcome.Success)

	if !sampled {
		if s.metrics != nil {
			s.metrics.EventsRejected.WithLabelValues("sampling").Inc()
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	slog.Info("event accepted",
		"trace_id", ev.Request.TraceID,
		"status_code", ev.Outcome.StatusCode,
		"success", ev.Outcome.Success,
		"error_code", errorCode(&ev),
	)

	// Build graph from event and merge into store
	mergeStart := time.Now()
	g := s.builder.Build(ev)
	if s.store != nil {
		s.store.Merge(g)
	}
	if s.metrics != nil {
		s.metrics.MergeLatency.Observe(time.Since(mergeStart).Seconds())
		s.metrics.EventsAccepted.Inc()
	}

	s.accepted.Add(1)
	w.WriteHeader(http.StatusAccepted)
}

// Validate handles POST /v1/events/validate — dry-run validation without ingestion.
func (s *Server) Validate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)

	var ev event.WideEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&ev); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "errors": []string{err.Error()}})
		return
	}

	if err := ev.Validate(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "errors": []string{err.Error()}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"valid": true})
}

// Store returns the server's graph store.
func (s *Server) Store() *store.Store {
	return s.store
}

// AcceptedCount returns the number of accepted events.
func (s *Server) AcceptedCount() uint64 {
	return s.accepted.Load()
}

// Builder returns the server's graph builder.
func (s *Server) Builder() *build.Builder {
	return s.builder
}

// EventSearch handles GET /v1/events/search.
func (s *Server) EventSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.EventLogDir == "" {
		http.Error(w, "event log not configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	traceID := q.Get("trace_id")
	userID := q.Get("user_id")
	service := q.Get("service")
	errorCode := q.Get("error_code")

	if traceID == "" && userID == "" && service == "" && errorCode == "" {
		http.Error(w, "at least one filter required (trace_id, user_id, service, error_code)", http.StatusBadRequest)
		return
	}

	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	f := eventlog.SearchFilter{
		TraceID:   traceID,
		UserID:    userID,
		Service:   service,
		ErrorCode: errorCode,
		Limit:     limit,
	}
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid start: must be RFC3339", http.StatusBadRequest)
			return
		}
		f.Start = t
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid end: must be RFC3339", http.StatusBadRequest)
			return
		}
		f.End = t
	}

	events, err := eventlog.Search(s.EventLogDir, f)
	if err != nil {
		slog.Error("event search failed", "err", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"events": events,
		"count":  len(events),
	})
}

// TraceStory handles GET /v1/traces/story?trace_id=<id>.
func (s *Server) TraceStory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	traceID := r.URL.Query().Get("trace_id")
	if traceID == "" {
		http.Error(w, "trace_id required", http.StatusBadRequest)
		return
	}

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	story, ctx, err := tracestory.Build(snap, traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"story":   story,
		"context": ctx,
	})
}

// traceEntry is a summary of a single request for the recent traces list.
type traceEntry struct {
	TraceID    string    `json:"trace_id"`
	Service    string    `json:"service,omitempty"`
	Success    bool      `json:"success"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	EventName  string    `json:"event_name,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// RecentTraces handles GET /v1/traces/recent?limit=<n>.
func (s *Server) RecentTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	entries := recentTracesFromGraph(snap, limit)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func recentTracesFromGraph(g *core.Graph, limit int) []traceEntry {
	var entries []traceEntry
	for _, n := range g.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		traceID, _ := n.Attr["trace_id"].(string)
		if traceID == "" {
			continue
		}
		e := traceEntry{
			TraceID:   traceID,
			Timestamp: n.LastSeen,
		}
		if v, ok := n.Attr["success"].(bool); ok {
			e.Success = v
		}
		if v, ok := n.Attr["status_code"]; ok {
			e.StatusCode = attrToInt(v)
		}
		if v, ok := n.Attr["latency_ms"]; ok {
			e.LatencyMs = attrToInt64(v)
		}
		if v, ok := n.Attr["event_name"].(string); ok {
			e.EventName = v
		}
		// Prefer root_service; fall back to event_name prefix.
		svc, _ := n.Attr["root_service"].(string)
		if svc == "" && e.EventName != "" {
			svc = e.EventName
			if dot := strings.IndexByte(e.EventName, '.'); dot > 0 {
				svc = e.EventName[:dot]
			}
		}
		e.Service = svc
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// Overview handles GET /v1/overview?window=<duration>&limit=<n>.
func (s *Server) Overview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}

	windowStr := r.URL.Query().Get("window")
	dur := 5 * time.Minute
	if windowStr != "" {
		if d, err := time.ParseDuration(windowStr); err == nil && d > 0 {
			dur = d
		}
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	now := time.Now()
	start := now.Add(-dur)
	summary := s.store.SummarizeWindow(start, now)

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	recent := recentTracesFromGraph(snap, limit)

	totalRequests := summary.TotalRequests
	totalFailures := summary.TotalFailures

	// Error rate from windowed unsampled counters (not skewed by sampling).
	// Falls back to graph-based counts when counters are zero (e.g. after
	// restart before new traffic arrives).
	errorRate := 0.0
	if unsampledTotal, unsampledErrors := s.counters.Sum(dur); unsampledTotal > 0 {
		errorRate = float64(unsampledErrors) / float64(unsampledTotal) * 100
	} else if totalRequests > 0 {
		errorRate = float64(totalFailures) / float64(totalRequests) * 100
	}

	// Top errors: count one primary error per failed request.
	// This avoids fan-out inflation where one failed request emits multiple
	// downstream error events across services.
	type errorEntry struct {
		Code  string `json:"code"`
		Count int    `json:"count"`
	}
	errorCountByCode := map[string]int{}
	for reqID, n := range snap.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if n.LastSeen.IsZero() || n.LastSeen.Before(start) || n.LastSeen.After(now) {
			continue
		}
		code, ok := primaryRequestErrorCode(snap, reqID, n)
		if !ok {
			continue
		}
		errorCountByCode[code]++
	}

	var topErrors []errorEntry
	for code, count := range errorCountByCode {
		topErrors = append(topErrors, errorEntry{Code: code, Count: count})
	}
	sort.Slice(topErrors, func(i, j int) bool {
		if topErrors[i].Count == topErrors[j].Count {
			return topErrors[i].Code < topErrors[j].Code
		}
		return topErrors[i].Count > topErrors[j].Count
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"window":         dur.String(),
		"total_requests": totalRequests,
		"total_failures": totalFailures,
		"error_rate":     errorRate,
		"sampled":        s.sampleRatePct < 100,
		"top_errors":     topErrors,
		"recent_traces":  recent,
	})
}

// OverviewTimeseries handles GET /v1/overview/timeseries?window=1h&step=5m.
func (s *Server) OverviewTimeseries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	window := 1 * time.Hour
	if v := q.Get("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			http.Error(w, "invalid window", http.StatusBadRequest)
			return
		}
		if d > 24*time.Hour {
			http.Error(w, "window max 24h", http.StatusBadRequest)
			return
		}
		window = d
	}

	step := 5 * time.Minute
	if v := q.Get("step"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			http.Error(w, "invalid step", http.StatusBadRequest)
			return
		}
		if d < 15*time.Second {
			http.Error(w, "step min 15s", http.StatusBadRequest)
			return
		}
		if d > 15*time.Minute {
			http.Error(w, "step max 15m", http.StatusBadRequest)
			return
		}
		step = d
	}

	points := int(window / step)
	if points > 1440 {
		http.Error(w, "too many points (window/step max 1440)", http.StatusBadRequest)
		return
	}

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}

	now := time.Now()
	start := now.Add(-window)

	type bucket struct {
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
		Total     int       `json:"total"`
		Failures  int       `json:"failures"`
		ErrorRate float64   `json:"error_rate"`
		Status2xx int       `json:"status_2xx"`
		Status4xx int       `json:"status_4xx"`
		Status5xx int       `json:"status_5xx"`
		P50       int64     `json:"p50"`
		P95       int64     `json:"p95"`
		P99       int64     `json:"p99"`
		latencies []int64
	}

	buckets := make([]bucket, points)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * step)
		buckets[i].End = buckets[i].Start.Add(step)
	}

	for _, n := range snap.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if n.LastSeen.Before(start) || n.LastSeen.After(now) {
			continue
		}
		idx := int(n.LastSeen.Sub(start) / step)
		if idx >= points {
			idx = points - 1
		}
		b := &buckets[idx]
		b.Total++

		sc := attrToInt(n.Attr["status_code"])
		switch {
		case sc >= 200 && sc < 300:
			b.Status2xx++
		case sc >= 400 && sc < 500:
			b.Status4xx++
		case sc >= 500:
			b.Status5xx++
		}

		success, _ := n.Attr["success"].(bool)
		if !success {
			b.Failures++
		}

		if lat := attrToInt64(n.Attr["latency_ms"]); lat > 0 {
			b.latencies = append(b.latencies, lat)
		}
	}

	// Compute percentiles and error rates.
	type bucketOut struct {
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
		Total     int       `json:"total"`
		Failures  int       `json:"failures"`
		ErrorRate float64   `json:"error_rate"`
		Status2xx int       `json:"status_2xx"`
		Status4xx int       `json:"status_4xx"`
		Status5xx int       `json:"status_5xx"`
		P50       int64     `json:"p50"`
		P95       int64     `json:"p95"`
		P99       int64     `json:"p99"`
	}

	out := make([]bucketOut, points)
	for i := range buckets {
		b := &buckets[i]
		out[i] = bucketOut{
			Start: b.Start, End: b.End,
			Total: b.Total, Failures: b.Failures,
			Status2xx: b.Status2xx, Status4xx: b.Status4xx, Status5xx: b.Status5xx,
		}
		if b.Total > 0 {
			out[i].ErrorRate = math.Round(float64(b.Failures)/float64(b.Total)*10000) / 100
		}
		if len(b.latencies) > 0 {
			sort.Slice(b.latencies, func(a, c int) bool { return b.latencies[a] < b.latencies[c] })
			out[i].P50 = percentile(b.latencies, 50)
			out[i].P95 = percentile(b.latencies, 95)
			out[i].P99 = percentile(b.latencies, 99)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sampled": s.sampleRatePct < 100,
		"buckets": out,
	})
}

func percentile(sorted []int64, pct int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(pct)/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Routes handles GET /v1/routes?window=5m&limit=20.
func (s *Server) Routes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	window := 5 * time.Minute
	if v := q.Get("window"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			window = d
		}
	}

	limit := 20
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}

	now := time.Now()
	start := now.Add(-window)

	type routeStats struct {
		Service       string
		Method        string
		RouteTemplate string
		Total         int
		Failures      int
		Status2xx     int
		Status4xx     int
		Status5xx     int
		latencies     []int64
	}

	groups := map[string]*routeStats{}

	for _, n := range snap.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if n.LastSeen.Before(start) || n.LastSeen.After(now) {
			continue
		}

		eventName, _ := n.Attr["event_name"].(string)
		if eventName == "" {
			continue
		}

		// Prefer root_service (set by merge when root span arrives).
		// Fall back to event_name prefix when root hasn't been merged yet.
		svc, _ := n.Attr["root_service"].(string)
		if svc == "" {
			svc = eventName
			if dot := strings.IndexByte(eventName, '.'); dot > 0 {
				svc = eventName[:dot]
			}
		}

		method, _ := n.Attr["http_method"].(string)
		if method == "" {
			method = "UNKNOWN"
		}
		routeTemplate, _ := n.Attr["route_template"].(string)
		if routeTemplate == "" {
			routeTemplate = eventName
		}

		key := svc + "\x00" + method + "\x00" + routeTemplate
		rs := groups[key]
		if rs == nil {
			rs = &routeStats{Service: svc, Method: method, RouteTemplate: routeTemplate}
			groups[key] = rs
		}

		rs.Total++

		sc := attrToInt(n.Attr["status_code"])
		switch {
		case sc >= 200 && sc < 300:
			rs.Status2xx++
		case sc >= 400 && sc < 500:
			rs.Status4xx++
		case sc >= 500:
			rs.Status5xx++
		}

		success, _ := n.Attr["success"].(bool)
		if !success {
			rs.Failures++
		}

		if lat := attrToInt64(n.Attr["latency_ms"]); lat > 0 {
			rs.latencies = append(rs.latencies, lat)
		}
	}

	type routeEntry struct {
		Service       string  `json:"service"`
		Method        string  `json:"method"`
		RouteTemplate string  `json:"route_template"`
		Route         string  `json:"route"` // deprecated: alias for route_template
		Invocations   int     `json:"invocations"`
		Errors        int     `json:"errors"`
		ErrorRate     float64 `json:"error_rate"`
		Status2xx     int     `json:"status_2xx"`
		Status4xx     int     `json:"status_4xx"`
		Status5xx     int     `json:"status_5xx"`
		P75LatencyMs  int64   `json:"p75_latency_ms"`
	}

	routes := make([]routeEntry, 0, len(groups))
	for _, rs := range groups {
		re := routeEntry{
			Service:       rs.Service,
			Method:        rs.Method,
			RouteTemplate: rs.RouteTemplate,
			Route:         rs.RouteTemplate,
			Invocations:   rs.Total,
			Errors:        rs.Failures,
			Status2xx:     rs.Status2xx,
			Status4xx:     rs.Status4xx,
			Status5xx:     rs.Status5xx,
		}
		if rs.Total > 0 {
			re.ErrorRate = math.Round(float64(rs.Failures)/float64(rs.Total)*10000) / 100
		}
		if len(rs.latencies) > 0 {
			sort.Slice(rs.latencies, func(a, b int) bool { return rs.latencies[a] < rs.latencies[b] })
			re.P75LatencyMs = percentile(rs.latencies, 75)
		}
		routes = append(routes, re)
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Invocations != routes[j].Invocations {
			return routes[i].Invocations > routes[j].Invocations
		}
		if routes[i].Route != routes[j].Route {
			return routes[i].Route < routes[j].Route
		}
		return routes[i].Method < routes[j].Method
	})

	if len(routes) > limit {
		routes = routes[:limit]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sampled": s.sampleRatePct < 100,
		"routes":  routes,
	})
}

// APIKeyMiddleware rejects requests that don't provide a valid API key
// via Authorization: Bearer <key> or X-API-Key header.
func APIKeyMiddleware(key string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			if strings.TrimPrefix(auth, "Bearer ") == key {
				next(w, r)
				return
			}
		}
		if r.Header.Get("X-API-Key") == key {
			next(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// CORSWrap wraps a handler with CORS headers scoped to read endpoints.
func CORSWrap(allowOrigin string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

func attrToInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case int64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	}
	return 0
}

func primaryRequestErrorCode(g *core.Graph, reqID string, req core.Node) (string, bool) {
	if req.Attr != nil {
		// Prefer singular request-level code (typically first failure encountered).
		if code, ok := req.Attr["error_code"].(string); ok && code != "" {
			return code, true
		}
	}

	codeSet := map[string]struct{}{}
	if req.Attr != nil {
		for _, c := range anyToStringSlice(req.Attr["error_codes"]) {
			if c != "" {
				codeSet[c] = struct{}{}
			}
		}
	}

	// Fallback: derive from request->error edges in case attrs are missing.
	if len(codeSet) == 0 {
		for _, e := range g.OutEdges[reqID] {
			if e.Type != core.EdgeFailedWith {
				continue
			}
			n, ok := g.Nodes[e.To]
			if !ok || n.Type != core.NodeError || n.Attr == nil {
				continue
			}
			if code, ok := n.Attr["code"].(string); ok && code != "" {
				codeSet[code] = struct{}{}
			}
		}
	}

	if len(codeSet) == 0 {
		return "", false
	}

	codes := make([]string, 0, len(codeSet))
	for c := range codeSet {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes[0], true
}

func anyToStringSlice(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func attrToInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func errorCode(ev *event.WideEvent) string {
	if ev.Error != nil {
		return ev.Error.Code
	}
	return ""
}

func (s *Server) snapshotOrServiceUnavailable(w http.ResponseWriter) (*core.Graph, bool) {
	if s.store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return nil, false
	}
	return s.store.Snapshot(), true
}
