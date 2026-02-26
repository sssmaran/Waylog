package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
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
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tools"
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
	store       *store.Store
	builder     *build.Builder
	sampler     *sampler.Sampler
	metrics     *metrics.Metrics
	EventLog    *eventlog.Writer
	EventLogDir string

	accepted      atomic.Uint64
	counters      unsampledCounters
	sampleRatePct int
	ready         atomic.Bool
	startTime     time.Time
	maxBodyBytes  int64

	askProvider         llm.Provider
	askRegistry         *tools.Registry
	askMaxStepsDefault  int
	askMaxStepsMax      int
	dashboardRefreshSec int
	prometheusURL       string
	grafanaURL          string

	// Replay state — set once during startup, read by /healthz.
	replayStatus      string // "none", "ok", "failed"
	replayError       string
	lastReplayAttempt time.Time
	lastReplaySuccess time.Time
}

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Store               *store.Store
	Sampler             *sampler.Sampler
	Metrics             *metrics.Metrics
	MaxBodyBytes        int64
	EventLogDir         string
	StartTime           time.Time
	SampleRatePct       int // 0 means use sampler's default from env
	AskProvider         llm.Provider
	AskRegistry         *tools.Registry
	AskMaxStepsDefault  int
	AskMaxStepsMax      int
	DashboardRefreshSec int
	PrometheusURL       string
	GrafanaURL          string
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
		store:               cfg.Store,
		builder:             build.NewBuilder(),
		sampler:             cfg.Sampler,
		metrics:             cfg.Metrics,
		maxBodyBytes:        maxBody,
		startTime:           startTime,
		EventLogDir:         cfg.EventLogDir,
		sampleRatePct:       cfg.SampleRatePct,
		askProvider:         cfg.AskProvider,
		askRegistry:         cfg.AskRegistry,
		askMaxStepsDefault:  cfg.AskMaxStepsDefault,
		askMaxStepsMax:      cfg.AskMaxStepsMax,
		dashboardRefreshSec: cfg.DashboardRefreshSec,
		prometheusURL:       cfg.PrometheusURL,
		grafanaURL:          cfg.GrafanaURL,
		replayStatus:        "none",
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
	}
	if s.sampleRatePct == 0 {
		s.sampleRatePct = s.sampler.HappySampleRatePct()
	}
	if s.askMaxStepsDefault <= 0 {
		s.askMaxStepsDefault = 5
	}
	if s.askMaxStepsMax <= 0 {
		s.askMaxStepsMax = 8
	}
	if s.askMaxStepsMax < s.askMaxStepsDefault {
		s.askMaxStepsMax = s.askMaxStepsDefault
	}
	if s.dashboardRefreshSec <= 0 {
		s.dashboardRefreshSec = 10
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

// Capabilities handles GET /v1/capabilities.
// It returns runtime capabilities/config used by UI clients.
func (s *Server) Capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	askEnabled, model, toolMode := s.askCapabilityState()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ask": map[string]any{
			"enabled":           askEnabled,
			"model":             model,
			"tool_mode":         toolMode,
			"max_steps_default": s.askMaxStepsDefault,
			"max_steps_max":     s.askMaxStepsMax,
		},
		"dashboard": map[string]any{
			"refresh_interval_sec": s.dashboardRefreshSec,
		},
		"links": map[string]any{
			"prometheus": s.prometheusURL,
			"grafana":    s.grafanaURL,
		},
	})
}

// Tools handles GET /v1/tools — returns available graph tools with examples.
func (s *Server) Tools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	registry := s.askRegistry
	if registry == nil {
		registry = tools.NewRegistry()
		if err := tools.RegisterGraphTools(registry); err != nil {
			http.Error(w, "tool registry unavailable", http.StatusInternalServerError)
			return
		}
	}

	type toolEntry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
		Examples    []string        `json:"examples,omitempty"`
	}

	list := registry.List()
	entries := make([]toolEntry, len(list))
	for i, t := range list {
		entries[i] = toolEntry{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Examples:    t.Examples,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"tools": entries,
		"count": len(entries),
	})
}

type askRequest struct {
	Prompt   string `json:"prompt"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

type askResponse struct {
	Answer     string        `json:"answer"`
	Model      string        `json:"model,omitempty"`
	ToolMode   string        `json:"tool_mode,omitempty"`
	DurationMs int64         `json:"duration_ms"`
	Steps      []askToolStep `json:"steps,omitempty"`
}

type askToolStep struct {
	Index      int    `json:"index"`
	Tool       string `json:"tool"`
	DurationMs int64  `json:"duration_ms"`
	Params     any    `json:"params,omitempty"`
	Result     any    `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
}

type overviewErrorEntry struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type overviewRollup struct {
	TotalRequests  int
	TotalFailures  int
	P50            int64
	P95            int64
	P99            int64
	TopErrorByCode map[string]int
}

// Ask handles POST /v1/ask and returns an LLM answer backed by graph tools.
func (s *Server) Ask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	var req askRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	var (
		provider llm.Provider
		model    string
		toolMode string
		err      error
	)
	if s.askProvider != nil {
		provider = s.askProvider
	} else {
		provider, model, toolMode, err = s.askProviderFromEnv()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	registry := s.askRegistry
	if registry == nil {
		registry = tools.NewRegistry()
		if err := tools.RegisterGraphTools(registry); err != nil {
			slog.Error("ask tool registry init failed", "err", err)
			http.Error(w, "tool registry unavailable", http.StatusInternalServerError)
			return
		}
	}

	defs := make([]llm.ToolDefinition, 0, len(registry.List()))
	for _, t := range registry.List() {
		defs = append(defs, llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	maxSteps := s.askMaxStepsDefault
	if req.MaxSteps > 0 {
		maxSteps = req.MaxSteps
	}
	if maxSteps < 1 {
		maxSteps = 1
	}
	if maxSteps > s.askMaxStepsMax {
		maxSteps = s.askMaxStepsMax
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	steps := make([]askToolStep, 0, maxSteps)
	answer, err := llm.Ask(ctx, provider, defs, llm.ToolExecutorFunc(func(ctx context.Context, name string, params json.RawMessage) (any, error) {
		stepStart := time.Now()
		step := askToolStep{
			Index:  len(steps) + 1,
			Tool:   name,
			Params: decodeJSONRaw(params),
		}
		result, callErr := registry.Call(ctx, s.store, name, params)
		step.DurationMs = time.Since(stepStart).Milliseconds()
		if callErr != nil {
			step.Error = callErr.Error()
			steps = append(steps, step)
			return nil, callErr
		}
		step.Result = normalizeJSONValue(result)
		steps = append(steps, step)
		return result, nil
	}), req.Prompt, maxSteps)
	if err != nil {
		slog.Warn("ask failed", "err", err)
		http.Error(w, "ask failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	resp := askResponse{
		Answer:     answer,
		Model:      model,
		ToolMode:   toolMode,
		DurationMs: time.Since(start).Milliseconds(),
		Steps:      steps,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	TraceID        string    `json:"trace_id"`
	Service        string    `json:"service,omitempty"`
	FailureService string    `json:"failure_service,omitempty"`
	Success        bool      `json:"success"`
	StatusCode     int       `json:"status_code"`
	LatencyMs      int64     `json:"latency_ms"`
	EventName      string    `json:"event_name,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// RecentTraces handles GET /v1/traces/recent?limit=<n>.
func (s *Server) RecentTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	limit := parseBoundedPositiveInt(q, "limit", 20, 100)
	failuresOnly := parseOptionalBool(q, "failures_only")

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	entries := recentTracesFromGraph(snap, limit, failuresOnly)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func recentTracesFromGraph(g *core.Graph, limit int, failuresOnly bool) []traceEntry {
	var entries []traceEntry
	for reqID, n := range g.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		traceID, _ := n.Attr["trace_id"].(string)
		if traceID == "" {
			continue
		}
		failed := requestNodeFailed(n)
		if failuresOnly && !failed {
			continue
		}
		e := traceEntry{
			TraceID:   traceID,
			Timestamp: n.LastSeen,
			Success:   !failed,
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
		e.Service = requestOwnerService(n.Attr, e.EventName)
		if failed {
			e.FailureService = requestFailureService(g, reqID, n)
		}
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

	q := r.URL.Query()
	dur := parseLooseDuration(q, "window", 5*time.Minute)
	limit := parseBoundedPositiveInt(q, "limit", 20, 100)

	now := time.Now()
	start := now.Add(-dur)
	summary := s.store.SummarizeWindow(start, now)

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	recent := recentTracesFromGraph(snap, limit, false)
	rollup := summarizeOverviewFromGraph(snap, start, now)

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

	var topErrors []overviewErrorEntry
	for code, count := range rollup.TopErrorByCode {
		topErrors = append(topErrors, overviewErrorEntry{Code: code, Count: count})
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
		"p50":            rollup.P50,
		"p95":            rollup.P95,
		"p99":            rollup.P99,
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

		addStatusClassCount(
			attrToInt(n.Attr["status_code"]),
			&b.Status2xx,
			&b.Status4xx,
			&b.Status5xx,
		)

		if requestNodeFailed(n) {
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

func summarizeOverviewFromGraph(g *core.Graph, start, end time.Time) overviewRollup {
	rollup := overviewRollup{TopErrorByCode: map[string]int{}}
	var latencies []int64
	for reqID, n := range g.Nodes {
		if n.Type != core.NodeRequest || n.LastSeen.IsZero() || n.LastSeen.Before(start) || n.LastSeen.After(end) {
			continue
		}
		rollup.TotalRequests++
		if requestNodeFailed(n) {
			rollup.TotalFailures++
		}
		if lat := attrToInt64(n.Attr["latency_ms"]); lat > 0 {
			latencies = append(latencies, lat)
		}
		if code, ok := primaryRequestErrorCode(g, reqID, n); ok {
			rollup.TopErrorByCode[code]++
		}
	}
	if len(latencies) == 0 {
		return rollup
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	rollup.P50 = percentile(latencies, 50)
	rollup.P95 = percentile(latencies, 95)
	rollup.P99 = percentile(latencies, 99)
	return rollup
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

	window := parseLooseDuration(q, "window", 5*time.Minute)
	limit := parseBoundedPositiveInt(q, "limit", 20, 100)
	failuresOnly := parseOptionalBool(q, "failures_only")

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
		failed := requestNodeFailed(n)
		if failuresOnly && !failed {
			continue
		}

		eventName, _ := n.Attr["event_name"].(string)
		if eventName == "" {
			continue
		}

		svc := requestOwnerService(n.Attr, eventName)

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

		addStatusClassCount(attrToInt(n.Attr["status_code"]), &rs.Status2xx, &rs.Status4xx, &rs.Status5xx)
		if failed {
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
		re.ErrorRate = percentage(rs.Failures, rs.Total)
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

func parseBoundedPositiveInt(q url.Values, key string, def, max int) int {
	n := def
	if v := q.Get(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > max {
		n = max
	}
	return n
}

func parseLooseDuration(q url.Values, key string, def time.Duration) time.Duration {
	d := def
	if v := q.Get(key); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil && parsed > 0 {
			d = parsed
		}
	}
	return d
}

func parseOptionalBool(q url.Values, key string) bool {
	v := q.Get(key)
	if v == "" {
		return false
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return parsed
}

func serviceFromEventName(eventName string) string {
	if dot := strings.IndexByte(eventName, '.'); dot > 0 {
		return eventName[:dot]
	}
	return eventName
}

func requestOwnerService(attr map[string]any, eventName string) string {
	if attr != nil {
		if svc, _ := attr["root_service"].(string); svc != "" {
			return svc
		}
	}
	if eventName == "" {
		return ""
	}
	return serviceFromEventName(eventName)
}

func addStatusClassCount(code int, status2xx, status4xx, status5xx *int) {
	switch {
	case code >= 200 && code < 300:
		*status2xx = *status2xx + 1
	case code >= 400 && code < 500:
		*status4xx = *status4xx + 1
	case code >= 500:
		*status5xx = *status5xx + 1
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

func requestFailureService(g *core.Graph, reqID string, req core.Node) string {
	if !requestNodeFailed(req) {
		return ""
	}
	bestService := ""
	bestTime := time.Time{}
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeRequestHasSpan {
			continue
		}
		span, ok := g.Nodes[e.To]
		if !ok || span.Type != core.NodeSpan {
			continue
		}
		if !requestNodeFailed(span) {
			continue
		}
		svc, _ := span.Attr["service"].(string)
		if svc == "" {
			continue
		}
		ts := span.LastSeen
		if bestService == "" || (!ts.IsZero() && (bestTime.IsZero() || ts.Before(bestTime))) {
			bestService = svc
			bestTime = ts
		}
	}
	if bestService != "" {
		return bestService
	}
	if svc, _ := req.Attr["root_service"].(string); svc != "" {
		return svc
	}
	if svc, _ := req.Attr["service"].(string); svc != "" {
		return svc
	}
	if eventName, _ := req.Attr["event_name"].(string); eventName != "" {
		return serviceFromEventName(eventName)
	}
	return ""
}

func requestNodeFailed(n core.Node) bool {
	if n.Attr == nil {
		return false
	}
	if success, ok := n.Attr["success"].(bool); ok && !success {
		return true
	}
	if statusCode := attrToInt(n.Attr["status_code"]); statusCode >= 500 {
		return true
	}
	if code, ok := n.Attr["error_code"].(string); ok && code != "" {
		return true
	}
	return len(anyToStringSlice(n.Attr["error_codes"])) > 0
}

func percentage(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return math.Round(float64(numerator)/float64(denominator)*10000) / 100
}

func decodeJSONRaw(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

func normalizeJSONValue(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return string(b)
	}
	return out
}

func (s *Server) askProviderFromEnv() (llm.Provider, string, string, error) {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	if key == "" {
		return nil, "", "", errors.New("gemini api key is not configured")
	}

	client := llm.NewGeminiClient(key)
	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	base := strings.TrimSpace(os.Getenv("GEMINI_API_BASE"))
	mode := strings.TrimSpace(os.Getenv("GEMINI_TOOL_MODE"))
	if model != "" {
		client.Model = model
	}
	if base != "" {
		client.BaseURL = base
	}
	if mode != "" {
		client.ToolMode = mode
	}
	return client, client.Model, client.ToolMode, nil
}

func (s *Server) askCapabilityState() (bool, string, string) {
	if s.askProvider != nil {
		return true, "", ""
	}
	_, model, toolMode, err := s.askProviderFromEnv()
	if err != nil {
		return false, "", ""
	}
	return true, model, toolMode
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
