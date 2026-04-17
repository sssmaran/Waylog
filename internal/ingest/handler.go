package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/detect"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
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
	traceStore  *tracestore.Store
	builder     *build.Builder
	sampler     *sampler.Sampler
	metrics     *metrics.Metrics
	EventLog    *eventlog.Writer
	EventLogDir string

	accepted       atomic.Uint64
	counters       unsampledCounters
	sampleRatePct  int
	ready          atomic.Bool
	startTime      time.Time
	maxBodyBytes   int64
	graphHotWindow time.Duration

	askProvider         llm.Provider
	askRegistry         *tools.Registry
	askMaxStepsDefault  int
	askMaxStepsMax      int
	dashboardRefreshSec int
	prometheusURL       string
	grafanaURL          string
	graphUI             bool
	dedupCache          *DedupCache
	agentKey            string
	trustProxy          bool
	coldWriter          *coldstore.BatchWriter
	coldStore           coldstore.Store
	planStore           *PlanStore

	// Dashboard rate limiter: per-IP sliding window
	rateMu         sync.Mutex
	rateLimit      map[string][]time.Time
	rateCheckCount int

	// Replay state — set once during startup, read by /healthz.
	replayStatus      string // "none", "ok", "failed"
	replayError       string
	lastReplayAttempt time.Time
	lastReplaySuccess time.Time

	// OTLP capability flag — reported by /v1/capabilities. Set via
	// ServerConfig when the OTLP handler is mounted in main.go.
	otlpEnabled bool

	// SSE
	sseHub               *SSEHub
	sseHeartbeatInterval time.Duration // configurable for testing, defaults to 15s

	// Causal engine status
	causalMu        sync.Mutex
	causalEnabled   bool
	causalLastRun   time.Time
	causalLastError string

	// Anomaly detector
	detector interface{ Current() *detect.Insight }
}

// SetSSEHub sets the SSE hub for real-time dashboard updates.
func (s *Server) SetSSEHub(hub *SSEHub) { s.sseHub = hub }

// SetDetector sets the anomaly detector for the /v1/insight endpoint.
func (s *Server) SetDetector(d interface{ Current() *detect.Insight }) { s.detector = d }

// SetCausalEnabled marks the causal engine as active.
// Called once at startup before HTTP traffic, no lock needed.
func (s *Server) SetCausalEnabled() { s.causalEnabled = true }

// SetCausalRunResult records the result of a causal inference tick.
// Called from the causal goroutine; reads happen from HTTP handlers (/healthz).
func (s *Server) SetCausalRunResult(err error) {
	s.causalMu.Lock()
	s.causalLastRun = time.Now()
	if err != nil {
		s.causalLastError = err.Error()
	} else {
		s.causalLastError = ""
	}
	s.causalMu.Unlock()
}

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Store               *store.Store
	TraceStore          *tracestore.Store
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
	GraphUI             bool
	DedupCache          *DedupCache
	AgentKey            string
	TrustProxy          bool
	ColdWriter          *coldstore.BatchWriter
	ColdStore           coldstore.Store
	PlanStore           *PlanStore
	GraphHotWindow      time.Duration
	OTLPEnabled         bool
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
		traceStore:          cfg.TraceStore,
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
		graphUI:             cfg.GraphUI,
		dedupCache:          cfg.DedupCache,
		agentKey:            cfg.AgentKey,
		trustProxy:          cfg.TrustProxy,
		coldWriter:          cfg.ColdWriter,
		coldStore:           cfg.ColdStore,
		planStore:           cfg.PlanStore,
		graphHotWindow:      cfg.GraphHotWindow,
		otlpEnabled:         cfg.OTLPEnabled,
		rateLimit:           map[string][]time.Time{},
		replayStatus:        "none",
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
	}
	if s.traceStore == nil {
		s.traceStore = tracestore.NewStore()
	}
	if s.graphHotWindow <= 0 {
		s.graphHotWindow, _ = runtimeGraphHotWindow()
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

	s.causalMu.Lock()
	causal := map[string]any{"enabled": s.causalEnabled}
	if !s.causalLastRun.IsZero() {
		causal["last_run"] = s.causalLastRun.Format(time.RFC3339)
	}
	if s.causalLastError != "" {
		causal["last_error"] = s.causalLastError
	}
	s.causalMu.Unlock()
	resp["causal"] = causal

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

	// Enqueue ALL accepted events to cold store (before sampling gate)
	// so /v1/events/search returns complete results regardless of sampling.
	if s.coldWriter != nil {
		s.coldWriter.Enqueue(ev)
	}

	// Auto-extract deployment from event (post-WAL, pre-sampling gate).
	// Uses detached context: event is already durable in WAL, client disconnect
	// must not abort the upsert.
	if ev.System.DeploymentID != "" && s.coldStore != nil {
		upsertCtx, upsertCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := s.coldStore.UpsertDeployment(upsertCtx, coldstore.Deployment{
			ID:        ev.System.DeploymentID,
			Service:   ev.System.Service,
			Version:   ev.System.Version,
			Env:       ev.System.Env,
			FirstSeen: ev.Timestamp,
			LastSeen:  ev.Timestamp,
		})
		upsertCancel()
		if err != nil {
			if !errors.Is(err, coldstore.ErrEnvConflict) && s.metrics != nil {
				s.metrics.DeployUpsertErrors.Inc()
			}
			slog.Warn("deployment auto-extract failed",
				"deployment_id", ev.System.DeploymentID,
				"err", err,
			)
		} else if s.metrics != nil {
			s.metrics.DeployUpsertsTotal.Inc()
		}
	}

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

	// Build graph + trace-store records from event and merge into derived views.
	mergeStart := time.Now()
	result := s.builder.BuildResult(ev)
	if s.store != nil {
		s.store.Merge(result.Graph)
	}
	if s.traceStore != nil && result.Span != nil {
		traceStart := time.Now()
		s.traceStore.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), result.Span)
		if s.metrics != nil {
			s.metrics.TraceUpsertDuration.Observe(time.Since(traceStart).Seconds())
		}
	}
	if s.sseHub != nil {
		s.sseHub.MarkDirty(TopicOverview, TopicRoutes, TopicTimeseries)
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

// Sampler returns the server's sampler so external wiring (e.g., OTLP
// pipeline construction in main.go) can share the same sampling policy.
func (s *Server) Sampler() *sampler.Sampler { return s.sampler }

// SSEHub returns the server's SSE hub for reuse as a Pipeline Notifier.
func (s *Server) SSEHub() *SSEHub { return s.sseHub }

// Counters returns the shared unsampled windowed counters. Used so the
// OTLP pipeline contributes to the same windowed error rate as the SDK path.
func (s *Server) Counters() *unsampledCounters { return &s.counters }

// AcceptedPtr returns a pointer to the accepted-events atomic counter so the
// shared pipeline can bump it in lockstep with the SDK Events() handler.
func (s *Server) AcceptedPtr() *atomic.Uint64 { return &s.accepted }

// SetOTLPEnabled toggles the OTLP capability flag reported by /v1/capabilities.
// Called once at startup after the OTLP route has been registered.
func (s *Server) SetOTLPEnabled(enabled bool) { s.otlpEnabled = enabled }

// EventSearch handles GET /v1/events/search.
// Both cold-store and JSONL paths return the same []coldstore.SearchResult shape.
func (s *Server) EventSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.coldStore == nil && s.EventLogDir == "" {
		http.Error(w, "event search not configured", http.StatusServiceUnavailable)
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

	limit := parseBoundedPositiveInt(q, "limit", 50, 200)

	cursorStr := q.Get("cursor")
	var cursorID int64
	if cursorStr != "" {
		var err error
		cursorID, err = decodeRowIDCursor(cursorStr)
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
	}

	var startTime, endTime time.Time
	if v := q.Get("start"); v != "" {
		t, err := parseFlexibleTime(v)
		if err != nil {
			http.Error(w, "invalid start: must be RFC3339", http.StatusBadRequest)
			return
		}
		startTime = t
	}
	if v := q.Get("end"); v != "" {
		t, err := parseFlexibleTime(v)
		if err != nil {
			http.Error(w, "invalid end: must be RFC3339", http.StatusBadRequest)
			return
		}
		endTime = t
	}

	// Prefer cold store (SQLite) over JSONL scan
	if s.coldStore != nil {
		page, err := s.coldStore.SearchEvents(coldstore.SearchFilter{
			TraceID:   traceID,
			UserID:    userID,
			Service:   service,
			ErrorCode: errorCode,
			Start:     startTime,
			End:       endTime,
			Limit:     limit,
			Cursor:    cursorID,
		})
		if err != nil {
			slog.Error("cold store search failed", "err", err)
			if s.EventLogDir == "" {
				http.Error(w, "search failed", http.StatusInternalServerError)
				return
			}
			// Fall through to JSONL fallback
		} else {
			if page.Results == nil {
				page.Results = []coldstore.SearchResult{}
			}
			resp := map[string]any{
				"events":      page.Results,
				"count":       len(page.Results),
				"total_count": page.TotalCount,
				"data_source": "sqlite",
			}
			if page.NextCursor > 0 {
				resp["next_cursor"] = encodeRowIDCursor(page.NextCursor)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	if s.EventLogDir == "" {
		http.Error(w, "event search not configured", http.StatusServiceUnavailable)
		return
	}

	// JSONL fallback does not support cursor pagination.
	if cursorID > 0 {
		http.Error(w, "cursor pagination not supported for event log fallback", http.StatusBadRequest)
		return
	}

	f := eventlog.SearchFilter{
		TraceID:   traceID,
		UserID:    userID,
		Service:   service,
		ErrorCode: errorCode,
		Limit:     limit,
		Start:     startTime,
		End:       endTime,
	}
	events, err := eventlog.Search(s.EventLogDir, f)
	if err != nil {
		slog.Error("event search failed", "err", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	// Convert WideEvent to SearchResult for consistent API shape.
	results := make([]coldstore.SearchResult, len(events))
	for i, ev := range events {
		var errCode, errMsg string
		if ev.Error != nil {
			errCode = ev.Error.Code
			errMsg = ev.Error.Message
		}
		results[i] = coldstore.SearchResult{
			TraceID:      ev.Request.TraceID,
			SpanID:       ev.Request.SpanID,
			EventName:    ev.EventName,
			Service:      ev.System.Service,
			Env:          ev.System.Env,
			Version:      ev.System.Version,
			DeploymentID: ev.System.DeploymentID,
			UserID:       ev.User.ID,
			StatusCode:   ev.Outcome.StatusCode,
			Success:      ev.Outcome.Success,
			ErrorCode:    errCode,
			ErrorMessage: errMsg,
			LatencyMs:    ev.Metrics.LatencyMs,
			Timestamp:    ev.Timestamp,
		}
	}

	resp := map[string]any{
		"events":      results,
		"count":       len(results),
		"total_count": len(results),
		"data_source": "event_log_fallback",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Capabilities handles GET /v1/capabilities.
// It returns runtime capabilities/config used by UI clients.
func (s *Server) Capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	askEnabled, model, toolMode := s.askCapabilityState()
	hotWindow := s.effectiveGraphHotWindow()
	_, hotWindowSource := runtimeGraphHotWindow()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ask": map[string]any{
			"enabled":           askEnabled,
			"model":             model,
			"tool_mode":         toolMode,
			"max_steps_default": s.askMaxStepsDefault,
			"max_steps_max":     s.askMaxStepsMax,
		},
		"ask_endpoint": "/ui/ask",
		"dashboard": map[string]any{
			"refresh_interval_sec": s.dashboardRefreshSec,
		},
		"links": map[string]any{
			"prometheus": s.prometheusURL,
			"grafana":    s.grafanaURL,
		},
		"graph": s.graphUI,
		"otlp": map[string]any{
			"http_traces": s.otlpEnabled,
		},
		"architecture": map[string]any{
			"flattened": true,
			"graph": map[string]any{
				"nodes": []string{"request", "service", "error"},
				"edges": []string{"handled_by", "failed_with", "calls"},
			},
			"trace_store": map[string]any{
				"enabled": s.traceStore != nil,
			},
			"hot_window": map[string]any{
				"enabled":       hotWindow > 0,
				"duration":      hotWindow.String(),
				"duration_secs": int64(hotWindow / time.Second),
				"source":        hotWindowSource,
			},
		},
	})
}

// Insight handles GET /v1/insight. Returns the current anomaly insight or 204.
func (s *Server) Insight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.detector == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	insight := s.detector.Current()
	if insight == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(insight)
}

func runtimeGraphHotWindow() (time.Duration, string) {
	if hot := config.GetenvDuration("GRAPH_HOT_WINDOW", 0); hot > 0 {
		return hot, "GRAPH_HOT_WINDOW"
	}
	if hot := config.GetenvDuration("GRAPH_RETENTION", 24*time.Hour); hot > 0 {
		return hot, "GRAPH_RETENTION"
	}
	return 24 * time.Hour, "default"
}

func (s *Server) effectiveGraphHotWindow() time.Duration {
	if s != nil && s.graphHotWindow > 0 {
		return s.graphHotWindow
	}
	hot, _ := runtimeGraphHotWindow()
	return hot
}

// Tools handles GET /v1/tools — returns available graph tools with examples.
func (s *Server) Tools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	registry := s.askRegistry
	if registry == nil {
		registry = tools.NewRegistry()
		if err := tools.RegisterGraphTools(registry); err != nil {
			respondError(w, r, http.StatusInternalServerError, "INTERNAL", "tool registry unavailable", true, APIMeta{RequestID: RequestIDFromContext(r.Context())})
			return
		}
	}

	type toolEntry struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		Version      string          `json:"version,omitempty"`
		InputSchema  json.RawMessage `json:"input_schema,omitempty"`
		OutputSchema json.RawMessage `json:"output_schema,omitempty"`
		Examples     []string        `json:"examples,omitempty"`
	}

	list := registry.List()
	entries := make([]toolEntry, len(list))
	for i, t := range list {
		entries[i] = toolEntry{
			Name:         t.Name,
			Description:  t.Description,
			Version:      t.Version,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Examples:     t.Examples,
		}
	}

	data := map[string]any{
		"tools": entries,
		"count": len(entries),
	}

	if wantsEnvelope(r) {
		reqID := RequestIDFromContext(r.Context())
		writeJSON(w, http.StatusOK, data, APIMeta{
			RequestID:  reqID,
			DataStatus: "complete",
		}, nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

type askRequest struct {
	Prompt        string `json:"prompt"`
	MaxSteps      int    `json:"max_steps,omitempty"`
	ErrorStrategy string `json:"error_strategy,omitempty"`
	TimeoutMs     int    `json:"timeout_ms,omitempty"`
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
		respondError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}
	if s.store == nil {
		respondError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "store not configured", true, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	reqID := RequestIDFromContext(r.Context())

	start := time.Now()
	var askHTTPStatus int
	var askErrCode = "none"
	defer func() {
		if s.metrics != nil && askHTTPStatus != 0 {
			if askErrCode != "none" && !allowedErrorLabels[askErrCode] {
				askErrCode = "INTERNAL"
			}
			s.metrics.AskRequestsTotal.WithLabelValues(strconv.Itoa(askHTTPStatus), askErrCode).Inc()
			s.metrics.AskDuration.Observe(time.Since(start).Seconds())
		}
	}()

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			askHTTPStatus = http.StatusRequestEntityTooLarge
			askErrCode = "PAYLOAD_TOO_LARGE"
			respondError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body too large", false, APIMeta{RequestID: reqID})
			return
		}
		askHTTPStatus = http.StatusBadRequest
		askErrCode = "INVALID_PARAMS"
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "failed to read body", false, APIMeta{RequestID: reqID})
		return
	}

	var req askRequest
	if err := json.Unmarshal(body, &req); err != nil {
		askHTTPStatus = http.StatusBadRequest
		askErrCode = "INVALID_PARAMS"
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "invalid json", false, APIMeta{RequestID: reqID})
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		askHTTPStatus = http.StatusBadRequest
		askErrCode = "INVALID_PARAMS"
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "prompt is required", false, APIMeta{RequestID: reqID})
		return
	}

	// Idempotency state — set by dedup block, read by completion paths
	var dedupIsExecutor, dedupCompleted bool

	// Idempotency check
	idempKey := r.Header.Get("Idempotency-Key")
	if idempKey != "" && s.dedupCache != nil {
		principal := s.dedupPrincipal(r)
		result, conflict, waitAborted := s.dedupCache.AcquireOrWait(r.Context(), r.Method, r.URL.Path, principal, idempKey, body)
		if waitAborted {
			keyHash := fmt.Sprintf("%.8s", fmt.Sprintf("%x", sha256.Sum256([]byte(idempKey))))
			if r.Context().Err() == context.DeadlineExceeded {
				askHTTPStatus = http.StatusGatewayTimeout
				askErrCode = "TIMEOUT"
				slog.Warn("dedup_waiter_timeout", "request_id", reqID, "method", r.Method, "path", r.URL.Path, "idempotency_key_hash", keyHash)
				respondError(w, r, http.StatusGatewayTimeout, "TIMEOUT", "request timeout while waiting for inflight request", true, APIMeta{RequestID: reqID})
			} else {
				slog.Warn("dedup_waiter_canceled", "request_id", reqID, "method", r.Method, "path", r.URL.Path, "idempotency_key_hash", keyHash)
				// Client canceled — no response write; askHTTPStatus stays 0 to skip counting
			}
			return
		} else if conflict {
			askHTTPStatus = http.StatusConflict
			askErrCode = "CONFLICT"
			respondError(w, r, http.StatusConflict, "CONFLICT", "idempotency key conflict: same key, different body", false, APIMeta{RequestID: reqID})
			return
		} else if result != nil {
			// Replay — don't count as a new request; askHTTPStatus stays 0
			if s.metrics != nil {
				s.metrics.DedupReplayTotal.Inc()
				s.metrics.DedupCacheSize.Set(float64(s.dedupCache.Size()))
			}
			s.replayDedupEntry(w, r, result, reqID)
			return
		}
		// Safety net: if we return before explicitly calling Complete,
		// release the inflight slot so waiters don't hang forever.
		dedupIsExecutor = true
		defer func() {
			if !dedupCompleted {
				principal := s.dedupPrincipal(r)
				s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body,
					http.StatusInternalServerError, nil, &APIError{Code: "INTERNAL", Message: "executor failed before completion", Retryable: true}, 0)
			}
			if s.metrics != nil {
				s.metrics.DedupCacheSize.Set(float64(s.dedupCache.Size()))
			}
		}()
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
			askHTTPStatus = http.StatusServiceUnavailable
			askErrCode = "SERVICE_UNAVAILABLE"
			if dedupIsExecutor {
				dedupCompleted = true
				principal := s.dedupPrincipal(r)
				s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body,
					http.StatusServiceUnavailable, nil, &APIError{Code: "SERVICE_UNAVAILABLE", Message: err.Error(), Retryable: true}, 0)
			}
			respondError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", err.Error(), true, APIMeta{RequestID: reqID})
			return
		}
	}

	registry := s.askRegistry
	if registry == nil {
		registry = tools.NewRegistry()
		if err := tools.RegisterGraphTools(registry); err != nil {
			slog.Error("ask tool registry init failed", "err", err)
			askHTTPStatus = http.StatusInternalServerError
			askErrCode = "INTERNAL"
			if dedupIsExecutor {
				dedupCompleted = true
				principal := s.dedupPrincipal(r)
				s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body,
					http.StatusInternalServerError, nil, &APIError{Code: "INTERNAL", Message: "tool registry unavailable", Retryable: true}, 0)
			}
			respondError(w, r, http.StatusInternalServerError, "INTERNAL", "tool registry unavailable", true, APIMeta{RequestID: reqID})
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

	// Clamp timeout: default 30s, min 5s, max 60s
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	if timeoutMs < 5000 {
		timeoutMs = 5000
	}
	if timeoutMs > 60000 {
		timeoutMs = 60000
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	fs := &frozenStore{snap: s.store.Snapshot(), real: s.store, ts: s.traceStore}
	answer, toolRecords, askErr := llm.Ask(ctx, provider, defs, llm.ToolExecutorFunc(func(ctx context.Context, name string, params json.RawMessage) (any, error) {
		return registry.Call(ctx, fs, name, params)
	}), req.Prompt, llm.AskOptions{MaxSteps: maxSteps, ErrorStrategy: req.ErrorStrategy})

	// Convert ToolCallRecords to askToolSteps
	steps := make([]askToolStep, 0, len(toolRecords))
	for i, rec := range toolRecords {
		step := askToolStep{
			Index:      i + 1,
			Tool:       rec.Name,
			DurationMs: rec.DurationMs,
			Params:     decodeJSONRaw(rec.Params),
			Error:      rec.Error,
		}
		if rec.Result != nil {
			step.Result = normalizeJSONValue(rec.Result)
		}
		steps = append(steps, step)
	}

	// Emit per-tool-call metrics (inline, not deferred — these are per-step)
	if s.metrics != nil {
		for _, rec := range toolRecords {
			toolStatus := "ok"
			if rec.Error != "" {
				toolStatus = "error"
			}
			s.metrics.AskToolCallsTotal.WithLabelValues(rec.Name, toolStatus).Inc()
			s.metrics.AskToolDuration.WithLabelValues(rec.Name).Observe(float64(rec.DurationMs) / 1000.0)
		}
	}

	if askErr != nil {
		askHTTPStatus = http.StatusBadGateway
		askErrCode = normalizeErrorCode(askErr)
		slog.Warn("ask failed", "err", askErr)
		errMsg := "ask failed: " + askErr.Error()
		if dedupIsExecutor {
			dedupCompleted = true
			principal := s.dedupPrincipal(r)
			s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body, http.StatusBadGateway,
				nil, &APIError{Code: askErrCode, Message: errMsg, Retryable: true}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusBadGateway, askErrCode, errMsg, true, APIMeta{RequestID: reqID, DurationMs: time.Since(start).Milliseconds(), DataStatus: "error"})
		return
	}

	askHTTPStatus = http.StatusOK

	resp := askResponse{
		Answer:     answer,
		Model:      model,
		ToolMode:   toolMode,
		DurationMs: time.Since(start).Milliseconds(),
		Steps:      steps,
	}

	if dedupIsExecutor {
		dedupCompleted = true
		principal := s.dedupPrincipal(r)
		s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body, http.StatusOK, resp, nil, time.Since(start).Milliseconds())
	}

	if wantsEnvelope(r) {
		writeJSON(w, http.StatusOK, resp, APIMeta{
			RequestID:  reqID,
			DurationMs: time.Since(start).Milliseconds(),
			DataStatus: "complete",
		}, nil)
		return
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
	story, ctx, err := tracestory.BuildWithTraceStore(snap, s.traceStore, traceID)
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

// RecentTraces handles GET /v1/traces/recent?limit=<n>&cursor=<cursor>.
func (s *Server) RecentTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	limit := parseBoundedPositiveInt(q, "limit", 20, 100)
	failuresOnly := parseOptionalBool(q, "failures_only")

	cursorStr := q.Get("cursor")
	var cursorTS time.Time
	var cursorTraceID string
	if cursorStr != "" {
		var err error
		cursorTS, cursorTraceID, err = decodeTimeCursor(cursorStr)
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
	}

	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	entries, totalCount, nextTS, nextTraceID := recentTracesFromGraphPaginated(snap, limit, failuresOnly, cursorTS, cursorTraceID)

	resp := map[string]any{
		"traces":      entries,
		"total_count": totalCount,
	}
	if !nextTS.IsZero() {
		resp["next_cursor"] = encodeTimeCursor(nextTS, nextTraceID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func recentTracesFromGraph(g *core.Graph, limit int, failuresOnly bool) []traceEntry {
	entries, _, _, _ := recentTracesFromGraphPaginated(g, limit, failuresOnly, time.Time{}, "")
	return entries
}

func recentTracesFromGraphPaginated(g *core.Graph, limit int, failuresOnly bool, cursorTS time.Time, cursorTraceID string) ([]traceEntry, int, time.Time, string) {
	var all []traceEntry
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
		all = append(all, e)
	}

	// Sort by (Timestamp DESC, TraceID DESC) for stable ordering.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].Timestamp.Equal(all[j].Timestamp) {
			return all[i].Timestamp.After(all[j].Timestamp)
		}
		return all[i].TraceID > all[j].TraceID
	})

	totalCount := len(all)

	// Apply cursor: skip entries at or "before" cursor in DESC order.
	if !cursorTS.IsZero() {
		idx := 0
		for idx < len(all) {
			e := all[idx]
			if e.Timestamp.Before(cursorTS) || (e.Timestamp.Equal(cursorTS) && e.TraceID < cursorTraceID) {
				break
			}
			idx++
		}
		all = all[idx:]
	}

	var nextTS time.Time
	var nextTraceID string
	if len(all) > limit {
		all = all[:limit]
		last := all[limit-1]
		nextTS = last.Timestamp
		nextTraceID = last.TraceID
	}
	return all, totalCount, nextTS, nextTraceID
}

// overviewPayload computes the overview data for a given window and trace limit.
// Shared by the Overview REST handler and SSE computeOverviewJSON.
func (s *Server) overviewPayload(dur time.Duration, limit int) map[string]any {
	now := time.Now()
	start := now.Add(-dur)
	summary := s.store.SummarizeWindow(start, now)
	snap := s.store.Snapshot()

	recent := recentTracesFromGraph(snap, limit, false)
	rollup := summarizeOverviewFromGraph(snap, start, now)

	totalRequests := summary.TotalRequests
	totalFailures := summary.TotalFailures

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

	latestFailedTraceID := latestFailedTrace(snap, start)

	return map[string]any{
		"window":                 dur.String(),
		"total_requests":         totalRequests,
		"total_failures":         totalFailures,
		"error_rate":             errorRate,
		"p50":                    rollup.P50,
		"p95":                    rollup.P95,
		"p99":                    rollup.P99,
		"sampled":                s.sampleRatePct < 100,
		"top_errors":             topErrors,
		"recent_traces":          recent,
		"latest_failed_trace_id": latestFailedTraceID,
	}
}

// latestFailedTrace finds the most recent failed trace ID in the snapshot
// that is newer than start.
func latestFailedTrace(snap *core.Graph, start time.Time) string {
	var latestID string
	var latestTime time.Time
	for _, n := range snap.Nodes {
		if n.Type != core.NodeRequest || n.LastSeen.Before(start) {
			continue
		}
		if success, _ := n.Attr["success"].(bool); success {
			continue
		}
		if n.LastSeen.After(latestTime) {
			latestTime = n.LastSeen
			if tid, ok := n.Attr["trace_id"].(string); ok {
				latestID = tid
			}
		}
	}
	return latestID
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

	payload := s.overviewPayload(dur, limit)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// timeseriesPayload computes bucketed timeseries data for a given window and step.
// Shared by the OverviewTimeseries REST handler and SSE computeTimeseriesJSON.
func (s *Server) timeseriesPayload(window, step time.Duration) map[string]any {
	points := int(window / step)

	snap := s.store.Snapshot()
	now := time.Now()
	start := now.Add(-window)

	type bucket struct {
		Start     time.Time
		End       time.Time
		Total     int
		Failures  int
		Status2xx int
		Status4xx int
		Status5xx int
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
		addStatusClassCount(attrToInt(n.Attr["status_code"]), &b.Status2xx, &b.Status4xx, &b.Status5xx)
		if requestNodeFailed(n) {
			b.Failures++
		}
		if lat := attrToInt64(n.Attr["latency_ms"]); lat > 0 {
			b.latencies = append(b.latencies, lat)
		}
	}

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

	return map[string]any{
		"sampled": s.sampleRatePct < 100,
		"buckets": out,
	}
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
		if d > s.effectiveGraphHotWindow() {
			http.Error(w, "window exceeds hot window", http.StatusBadRequest)
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

	if s.store == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}

	payload := s.timeseriesPayload(window, step)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
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

// routesPayload computes per-route stats for a given window and limit.
// Shared by the Routes REST handler and SSE computeRoutesJSON.
func (s *Server) routesPayload(window time.Duration, limit int, failuresOnly bool) map[string]any {
	snap := s.store.Snapshot()
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
		Route         string  `json:"route"`
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

	return map[string]any{
		"sampled": s.sampleRatePct < 100,
		"routes":  routes,
	}
}

// Routes handles GET /v1/routes?window=5m&limit=20.
func (s *Server) Routes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, ok := s.snapshotOrServiceUnavailable(w); !ok {
		return
	}

	q := r.URL.Query()
	window := parseLooseDuration(q, "window", 5*time.Minute)
	limit := parseBoundedPositiveInt(q, "limit", 20, 100)
	failuresOnly := parseOptionalBool(q, "failures_only")

	payload := s.routesPayload(window, limit, failuresOnly)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

// GraphTopology handles GET /v1/graph/topology?window=1h.
// Returns Cytoscape-formatted service topology: service nodes with aggregate
// stats and edges derived from span caller→service pairs.
func (s *Server) GraphTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	window := parseLooseDuration(q, "window", 1*time.Hour)
	if maxWindow := s.effectiveGraphHotWindow(); window > maxWindow {
		window = maxWindow
	}

	if _, ok := s.snapshotOrServiceUnavailable(w); !ok {
		return
	}

	now := time.Now()
	result := analysis.BuildTopology(s.store, s.traceStore, now.Add(-window), now)
	cyto := analysis.ToCytoscapeFormat(result)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cyto)
}

// CORSWrap wraps a handler with CORS headers.
// methods should be e.g. "GET, OPTIONS" or "POST, OPTIONS".
func CORSWrap(allowOrigin string, methods string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", methods)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Idempotency-Key, X-API-Key, X-Request-ID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Waylog-API-Version")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

// CorrelationIDMiddleware reads X-Request-ID or generates one, stores in context,
// and sets X-Request-ID on the response.
func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = generateRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := ContextWithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

// parseFlexibleTime accepts both RFC3339 and RFC3339Nano (fractional seconds).
func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
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

// frozenStore captures a snapshot once and reuses it across tool calls within a single request.
type frozenStore struct {
	snap *core.Graph
	real *store.Store
	ts   *tracestore.Store
}

func (f *frozenStore) Snapshot() *core.Graph { return f.snap }
func (f *frozenStore) SummarizeWindow(start, end time.Time) store.WindowSummary {
	return f.real.SummarizeWindow(start, end)
}
func (f *frozenStore) ForEachRequestFact(start, end time.Time, fn func(store.RequestFacts)) {
	f.real.ForEachRequestFact(start, end, fn)
}
func (f *frozenStore) ErrorIndex(errorCode string) ([]string, bool) {
	return f.real.ErrorIndex(errorCode)
}
func (f *frozenStore) TraceStore() *tracestore.Store {
	return f.ts
}

func toolErrorToHTTPStatus(te *tools.ToolError) int {
	switch te.Code {
	case tools.CodeInvalidParams:
		return http.StatusBadRequest
	case tools.CodeNotFound:
		return http.StatusNotFound
	case tools.CodeEmptyResult:
		return http.StatusOK
	case tools.CodeTimeout:
		return http.StatusGatewayTimeout
	case tools.CodeInternal:
		return http.StatusInternalServerError
	case tools.CodeGraphEmpty:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// dedupPrincipal returns a principal identifier for idempotency grouping.
func (s *Server) dedupPrincipal(r *http.Request) string {
	if s.agentKey != "" {
		h := sha256.Sum256([]byte(s.agentKey))
		return hex.EncodeToString(h[:])
	}
	return clientIP(r, s.trustProxy)
}

// normalizeErrorCode extracts a metric-safe error code label from an error.
// allowedErrorLabels is the bounded set of error code metric labels.
var allowedErrorLabels = map[string]bool{
	tools.CodeInvalidParams: true,
	tools.CodeNotFound:      true,
	tools.CodeEmptyResult:   true,
	tools.CodeTimeout:       true,
	tools.CodeInternal:      true,
	tools.CodeGraphEmpty:    true,
	"PROVIDER_ERROR":        true,
	"PAYLOAD_TOO_LARGE":     true,
	"CONFLICT":              true,
	"SERVICE_UNAVAILABLE":   true,
	"METHOD_NOT_ALLOWED":    true,
	"UNAUTHORIZED":          true,
}

func normalizeErrorCode(err error) string {
	var pe *llm.ProviderError
	if errors.As(err, &pe) {
		return "PROVIDER_ERROR"
	}
	if te, ok := tools.AsToolError(err); ok {
		if allowedErrorLabels[te.Code] {
			return te.Code
		}
		return "INTERNAL"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "TIMEOUT"
	}
	return "INTERNAL"
}

// ToolCall handles POST /v1/tools/{name} — direct tool invocation.
func (s *Server) ToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}
	if s.store == nil {
		respondError(w, r, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "store not configured", true, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	toolName := strings.TrimPrefix(r.URL.Path, "/v1/tools/")
	if toolName == "" {
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "tool name required", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	registry := s.askRegistry
	if registry == nil {
		respondError(w, r, http.StatusInternalServerError, "INTERNAL", "tool registry unavailable", true, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	reqID := RequestIDFromContext(r.Context())

	start := time.Now()
	var toolHTTPStatus int
	var toolStatusLabel = "ok"
	defer func() {
		if s.metrics != nil && toolHTTPStatus != 0 {
			s.metrics.ToolDirectCallsTotal.WithLabelValues(toolName, toolStatusLabel).Inc()
		}
	}()

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			toolHTTPStatus = http.StatusRequestEntityTooLarge
			toolStatusLabel = "error"
			respondError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body too large", false, APIMeta{RequestID: reqID})
			return
		}
		toolHTTPStatus = http.StatusBadRequest
		toolStatusLabel = "error"
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "failed to read body", false, APIMeta{RequestID: reqID})
		return
	}

	var params json.RawMessage
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		params = json.RawMessage(`{}`)
	} else {
		if trimmed[0] != '{' {
			toolHTTPStatus = http.StatusBadRequest
			toolStatusLabel = "error"
			respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "params must be a JSON object", false, APIMeta{RequestID: reqID})
			return
		}
		if err := json.Unmarshal(trimmed, &params); err != nil {
			toolHTTPStatus = http.StatusBadRequest
			toolStatusLabel = "error"
			respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "invalid json body", false, APIMeta{RequestID: reqID})
			return
		}
	}

	// Idempotency state — set by dedup block, read by completion paths
	var dedupIsExecutor, dedupCompleted bool

	// Idempotency check
	idempKey := r.Header.Get("Idempotency-Key")
	if idempKey != "" && s.dedupCache != nil {
		principal := s.dedupPrincipal(r)
		result, conflict, waitAborted := s.dedupCache.AcquireOrWait(r.Context(), r.Method, r.URL.Path, principal, idempKey, body)
		if waitAborted {
			keyHash := fmt.Sprintf("%.8s", fmt.Sprintf("%x", sha256.Sum256([]byte(idempKey))))
			if r.Context().Err() == context.DeadlineExceeded {
				toolHTTPStatus = http.StatusGatewayTimeout
				toolStatusLabel = "error"
				slog.Warn("dedup_waiter_timeout", "request_id", reqID, "method", r.Method, "path", r.URL.Path, "idempotency_key_hash", keyHash)
				respondError(w, r, http.StatusGatewayTimeout, "TIMEOUT", "request timeout while waiting for inflight request", true, APIMeta{RequestID: reqID})
			} else {
				slog.Warn("dedup_waiter_canceled", "request_id", reqID, "method", r.Method, "path", r.URL.Path, "idempotency_key_hash", keyHash)
				// Client canceled — no response write; toolHTTPStatus stays 0 to skip counting
			}
			return
		} else if conflict {
			toolHTTPStatus = http.StatusConflict
			toolStatusLabel = "error"
			respondError(w, r, http.StatusConflict, "CONFLICT", "idempotency key conflict: same key, different body", false, APIMeta{RequestID: reqID})
			return
		} else if result != nil {
			// Replay — don't count as a new request; toolHTTPStatus stays 0
			if s.metrics != nil {
				s.metrics.DedupReplayTotal.Inc()
				s.metrics.DedupCacheSize.Set(float64(s.dedupCache.Size()))
			}
			s.replayDedupEntry(w, r, result, reqID)
			return
		}
		// Safety net: if we return before explicitly calling Complete,
		// release the inflight slot so waiters don't hang forever.
		dedupIsExecutor = true
		defer func() {
			if !dedupCompleted {
				principal := s.dedupPrincipal(r)
				s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body,
					http.StatusInternalServerError, nil, &APIError{Code: "INTERNAL", Message: "executor failed before completion", Retryable: true}, 0)
			}
			if s.metrics != nil {
				s.metrics.DedupCacheSize.Set(float64(s.dedupCache.Size()))
			}
		}()
	}

	fs := &frozenStore{snap: s.store.Snapshot(), real: s.store, ts: s.traceStore}
	result, err := registry.Call(r.Context(), fs, toolName, params)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		toolStatusLabel = "error"
		te, ok := tools.AsToolError(err)
		if !ok {
			te = &tools.ToolError{Code: tools.CodeInternal, Message: err.Error()}
		}
		toolHTTPStatus = toolErrorToHTTPStatus(te)
		if dedupIsExecutor {
			dedupCompleted = true
			principal := s.dedupPrincipal(r)
			s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body, toolHTTPStatus, nil, &APIError{
				Code: te.Code, Message: te.Message, Retryable: te.Retryable,
			}, duration)
		}
		respondError(w, r, toolHTTPStatus, te.Code, te.Message, te.Retryable, APIMeta{
			RequestID:  reqID,
			DurationMs: duration,
			DataStatus: "error",
		})
		return
	}

	toolHTTPStatus = http.StatusOK

	if dedupIsExecutor {
		dedupCompleted = true
		principal := s.dedupPrincipal(r)
		s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body, http.StatusOK, result, nil, duration)
	}

	if wantsEnvelope(r) {
		writeJSON(w, http.StatusOK, result, APIMeta{
			RequestID:  reqID,
			DurationMs: duration,
			DataStatus: "complete",
		}, nil)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// replayDedupEntry writes a cached dedup entry back to the client.
func (s *Server) replayDedupEntry(w http.ResponseWriter, r *http.Request, entry *dedupEntry, reqID string) {
	meta := APIMeta{
		RequestID:  reqID,
		DurationMs: entry.DurationMs,
		DataStatus: "complete",
		Cached:     true,
	}
	if entry.Err != nil {
		meta.DataStatus = "error"
	}
	if wantsEnvelope(r) {
		writeJSON(w, entry.Status, entry.Data, meta, entry.Err)
		return
	}
	// Non-envelope: replay error as plain text (matching http.Error semantics)
	if entry.Err != nil {
		http.Error(w, entry.Err.Message, entry.Status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(entry.Status)
	json.NewEncoder(w).Encode(entry.Data)
}

// DashboardAsk handles POST /ui/ask — rate-limited ask proxy for the web dashboard.
func (s *Server) DashboardAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Per-IP rate limit: 5 req/min
	ip := clientIP(r, s.trustProxy)
	if !s.checkRateLimit(ip) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
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
		http.Error(w, "tool registry unavailable", http.StatusInternalServerError)
		return
	}

	defs := make([]llm.ToolDefinition, 0, len(registry.List()))
	for _, t := range registry.List() {
		defs = append(defs, llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	// Force max_steps=5, abort on error
	maxSteps := 5

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	fs := &frozenStore{snap: s.store.Snapshot(), real: s.store, ts: s.traceStore}
	answer, toolRecords, askErr := llm.Ask(ctx, provider, defs, llm.ToolExecutorFunc(func(ctx context.Context, name string, params json.RawMessage) (any, error) {
		return registry.Call(ctx, fs, name, params)
	}), req.Prompt, llm.AskOptions{MaxSteps: maxSteps, ErrorStrategy: "abort"})

	steps := make([]askToolStep, 0, len(toolRecords))
	for i, rec := range toolRecords {
		step := askToolStep{
			Index:      i + 1,
			Tool:       rec.Name,
			DurationMs: rec.DurationMs,
			Params:     decodeJSONRaw(rec.Params),
			Error:      rec.Error,
		}
		if rec.Result != nil {
			step.Result = normalizeJSONValue(rec.Result)
		}
		steps = append(steps, step)
	}

	if askErr != nil {
		slog.Warn("dashboard ask failed", "err", askErr)
		http.Error(w, "ask failed: "+askErr.Error(), http.StatusBadGateway)
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

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if parts := strings.SplitN(xff, ",", 2); len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) checkRateLimit(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-time.Minute)

	s.rateMu.Lock()
	defer s.rateMu.Unlock()

	// Periodic global prune every 100 calls
	s.rateCheckCount++
	if s.rateCheckCount%100 == 0 {
		for k, v := range s.rateLimit {
			allStale := true
			for _, ts := range v {
				if ts.After(cutoff) {
					allStale = false
					break
				}
			}
			if allStale {
				delete(s.rateLimit, k)
			}
		}
	}

	// Hard bound: reject if map still too large after pruning
	if len(s.rateLimit) > 10000 {
		return false
	}

	// Prune stale entries for this IP
	timestamps := s.rateLimit[ip]
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= 5 {
		s.rateLimit[ip] = valid
		return false
	}

	s.rateLimit[ip] = append(valid, now)
	return true
}

// Topology handles GET /v1/topology — service-to-service edges with failure counts.
func (s *Server) Topology(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reqStart := time.Now()
	meta := APIMeta{RequestID: RequestIDFromContext(r.Context()), APIVersion: apiVersion}

	q := r.URL.Query()
	dur := parseLooseDuration(q, "window", time.Hour)
	if maxWindow := s.effectiveGraphHotWindow(); dur > maxWindow {
		dur = maxWindow
	}
	if _, ok := s.snapshotOrServiceUnavailable(w); !ok {
		return
	}

	now := time.Now()
	result := analysis.BuildTopology(s.store, s.traceStore, now.Add(-dur), now)

	meta.DurationMs = time.Since(reqStart).Milliseconds()
	meta.DataStatus = "complete"
	if len(result.Nodes) == 0 {
		meta.DataStatus = "empty"
	}
	writeJSON(w, http.StatusOK, result, meta, nil)
}

// BlastRadius handles GET /v1/blast_radius?error_code=X — impact analysis for an error code.
func (s *Server) BlastRadius(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reqStart := time.Now()
	meta := APIMeta{RequestID: RequestIDFromContext(r.Context()), APIVersion: apiVersion}

	errorCode := r.URL.Query().Get("error_code")
	if errorCode == "" {
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "error_code is required", false, meta)
		return
	}
	q := r.URL.Query()
	dur := parseLooseDuration(q, "window", time.Hour)
	if maxWindow := s.effectiveGraphHotWindow(); dur > maxWindow {
		dur = maxWindow
	}
	snap, ok := s.snapshotOrServiceUnavailable(w)
	if !ok {
		return
	}
	now := time.Now()
	result := analysis.ComputeBlastRadius(snap, errorCode, now.Add(-dur), now)

	meta.DurationMs = time.Since(reqStart).Milliseconds()
	meta.DataStatus = "complete"
	if result.AffectedRequests == 0 {
		meta.DataStatus = "empty"
	}
	writeJSON(w, http.StatusOK, result, meta, nil)
}

// DashboardExplain handles GET /ui/explain?trace_id=X — server-side proxy for explain_request tool.
// No agent auth required (same pattern as /ui/ask).
func (s *Server) DashboardExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	meta := APIMeta{RequestID: RequestIDFromContext(r.Context())}

	traceID := r.URL.Query().Get("trace_id")
	if traceID == "" {
		respondError(w, r, http.StatusBadRequest, "INVALID_PARAMS", "trace_id is required", false, meta)
		return
	}

	if s.askRegistry == nil {
		respondError(w, r, http.StatusServiceUnavailable, "NOT_AVAILABLE", "tools not available", false, meta)
		return
	}

	params, _ := json.Marshal(map[string]string{"trace_id": traceID})
	result, err := s.askRegistry.Call(r.Context(), s.store, "explain_request", params)
	if err != nil {
		var te *tools.ToolError
		if errors.As(err, &te) {
			status := http.StatusInternalServerError
			if te.Code == tools.CodeNotFound {
				status = http.StatusNotFound
			} else if te.Code == tools.CodeInvalidParams {
				status = http.StatusBadRequest
			}
			respondError(w, r, status, te.Code, te.Message, te.Retryable, meta)
			return
		}
		respondError(w, r, http.StatusInternalServerError, "INTERNAL", "internal error", true, meta)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// PlanExecute handles POST /v1/plans/execute — deterministic plan execution.
func (s *Server) PlanExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
	}

	reqID := RequestIDFromContext(r.Context())
	start := time.Now()

	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBodyBytes))
	if err != nil {
		respondError(w, r, http.StatusBadRequest, "BAD_REQUEST", "failed to read body", false, APIMeta{RequestID: reqID})
		return
	}

	// Idempotency state — set by dedup block, read by completion paths
	var dedupIsExecutor, dedupCompleted bool

	idempKey := r.Header.Get("Idempotency-Key")
	if idempKey != "" && s.dedupCache != nil {
		principal := s.dedupPrincipal(r)
		entry, conflict, aborted := s.dedupCache.AcquireOrWait(r.Context(), r.Method, r.URL.Path, principal, idempKey, body)
		if aborted {
			respondError(w, r, http.StatusServiceUnavailable, "TIMEOUT", "request timeout waiting for idempotent result", true, APIMeta{RequestID: reqID})
			return
		}
		if conflict {
			respondError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "same idempotency key with different body", false, APIMeta{RequestID: reqID})
			return
		}
		if entry != nil {
			s.replayDedupEntry(w, r, entry, reqID)
			return
		}
		// Safety net: if we return before explicitly calling Complete,
		// release the inflight slot so waiters don't hang forever.
		dedupIsExecutor = true
		defer func() {
			if !dedupCompleted {
				principal := s.dedupPrincipal(r)
				s.dedupCache.Complete(r.Method, r.URL.Path, principal, idempKey, body,
					http.StatusInternalServerError, nil, &APIError{Code: "INTERNAL", Message: "executor failed before completion", Retryable: true}, 0)
			}
		}()
	}

	var req struct {
		Steps []PlanStep `json:"steps"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		if dedupIsExecutor {
			dedupCompleted = true
			s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body,
				http.StatusBadRequest, nil, &APIError{Code: "BAD_REQUEST", Message: "invalid JSON: " + err.Error()}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error(), false, APIMeta{RequestID: reqID})
		return
	}

	registry := s.askRegistry
	if registry == nil {
		if dedupIsExecutor {
			dedupCompleted = true
			s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body,
				http.StatusInternalServerError, nil, &APIError{Code: "INTERNAL", Message: "tool registry not configured", Retryable: true}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusInternalServerError, "INTERNAL", "tool registry not configured", true, APIMeta{RequestID: reqID})
		return
	}

	if errs := ValidatePlan(req.Steps, registry); len(errs) > 0 {
		msg := strings.Join(errs, "; ")
		if dedupIsExecutor {
			dedupCompleted = true
			s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body,
				http.StatusBadRequest, nil, &APIError{Code: "INVALID_PLAN", Message: msg}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusBadRequest, "INVALID_PLAN", msg, false, APIMeta{RequestID: reqID})
		return
	}

	var planID string
	if s.planStore != nil {
		planID = s.planStore.Create()
	}

	result := s.executePlanWithProgress(r.Context(), req.Steps, registry, planID)

	if result.PlanID != "" {
		w.Header().Set("X-Plan-ID", result.PlanID)
	}

	dur := time.Since(start).Milliseconds()
	meta := APIMeta{RequestID: reqID, DurationMs: dur, DataStatus: "complete"}

	if dedupIsExecutor {
		dedupCompleted = true
		s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body, http.StatusOK, result, nil, dur)
	}

	if wantsEnvelope(r) {
		writeJSON(w, http.StatusOK, result, meta, nil)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func (s *Server) executePlanWithProgress(ctx context.Context, steps []PlanStep, registry *tools.Registry, planID string) *PlanResult {
	outputs := make(map[string]json.RawMessage)
	result := &PlanResult{
		PlanID: planID,
		Steps:  make([]PlanStepResult, 0, len(steps)),
		Total:  len(steps),
	}
	if planID == "" {
		result.PlanID = generatePlanID()
	}

	for i, step := range steps {
		if s.planStore != nil && planID != "" {
			startData, _ := json.Marshal(map[string]any{"index": i, "id": step.ID, "tool": step.Tool})
			s.planStore.Publish(planID, PlanEvent{Type: "step_start", Data: startData})
		}

		stepResult := PlanStepResult{ID: step.ID, Index: i, Tool: step.Tool}
		stepStart := time.Now()

		params := step.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		refs, err := FindRefs(params)
		if err == nil && len(refs) > 0 {
			params, err = ResolveParams(params, refs, outputs)
		}
		if err != nil {
			stepResult.DurationMs = time.Since(stepStart).Milliseconds()
			stepResult.Error = &PlanStepError{Code: "REF_RESOLVE_FAILED", Message: err.Error()}
			s.haltPlan(result, stepResult, i, planID)
			return result
		}

		toolResult, err := registry.Call(ctx, s.store, step.Tool, params)
		stepResult.DurationMs = time.Since(stepStart).Milliseconds()

		if err != nil {
			stepErr := &PlanStepError{Code: "TOOL_ERROR", Message: err.Error()}
			if te, ok := tools.AsToolError(err); ok {
				stepErr.Code = te.Code
				stepErr.Retryable = te.Retryable
			}
			stepResult.Error = stepErr
			s.haltPlan(result, stepResult, i, planID)
			return result
		}

		stepResult.Result = toolResult
		result.Steps = append(result.Steps, stepResult)
		raw, _ := json.Marshal(toolResult)
		outputs[step.ID] = json.RawMessage(raw)

		s.publishStepComplete(planID, stepResult)
	}

	result.Completed = len(steps)
	result.Status = "complete"
	s.completePlan(planID, result)
	return result
}

func (s *Server) haltPlan(result *PlanResult, stepResult PlanStepResult, i int, planID string) {
	result.Steps = append(result.Steps, stepResult)
	haltIdx := i
	result.HaltedAt = &haltIdx
	result.Error = stepResult.Error
	result.Completed = i
	result.Status = statusForHalt(i)
	s.publishStepComplete(planID, stepResult)
	s.completePlan(planID, result)
}

func (s *Server) publishStepComplete(planID string, sr PlanStepResult) {
	if s.planStore == nil || planID == "" {
		return
	}
	data := map[string]any{
		"index":       sr.Index,
		"id":          sr.ID,
		"tool":        sr.Tool,
		"duration_ms": sr.DurationMs,
	}
	if sr.Error != nil {
		data["status"] = "error"
		data["error"] = map[string]any{"code": sr.Error.Code, "message": sr.Error.Message}
	} else {
		data["status"] = "ok"
	}
	raw, _ := json.Marshal(data)
	s.planStore.Publish(planID, PlanEvent{Type: "step_complete", Data: raw})
}

func (s *Server) completePlan(planID string, result *PlanResult) {
	if s.planStore == nil || planID == "" {
		return
	}
	s.planStore.Complete(planID, result)
}

// PlanStream handles GET /v1/stream/plans/{id} — SSE progress stream.
func (s *Server) PlanStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	planID := strings.TrimPrefix(r.URL.Path, "/v1/stream/plans/")
	if planID == "" {
		http.Error(w, "plan ID required", http.StatusBadRequest)
		return
	}

	if s.planStore == nil {
		http.Error(w, "plan store not configured", http.StatusNotFound)
		return
	}

	ch, subID, ok := s.planStore.Subscribe(planID)
	if !ok {
		http.Error(w, "plan not found or expired", http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	writeSSEHeaders(w)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.planStore.Unsubscribe(planID, subID)
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
			flusher.Flush()
			if ev.Type == "done" {
				return
			}
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
