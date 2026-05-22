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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/detect"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tools"
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
// new events ingest correctly but historical reads (trace story, errors,
// blast radius) may return partial results until the v2 reader is rebuilt
// from incoming traffic.
type Server struct {
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
	dedupCache          *DedupCache
	agentKey            string
	trustProxy          bool
	coldWriter          *coldstore.BatchWriter
	coldStore           coldstore.Store
	planStore           *PlanStore

	// Replay state — set once during startup, read by /healthz.
	replayStatus      string // "none", "ok", "failed"
	replayError       string
	lastReplayAttempt time.Time
	lastReplaySuccess time.Time

	// OTLP capability flag — reported by /v1/capabilities. Set via
	// ServerConfig when the OTLP handler is mounted in main.go.
	otlpEnabled               bool
	otlpGRPCEnabled           bool
	otlpGRPCAddr              string
	incidentsEnabled          bool
	incidentsPersistent       bool
	incidentsRebuildSupported bool
	profile                   string

	// Anomaly detector
	detector interface{ Current() *detect.Insight }
}

// SetDetector sets the anomaly detector for the /v1/insight endpoint.
func (s *Server) SetDetector(d interface{ Current() *detect.Insight }) { s.detector = d }

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Sampler                  *sampler.Sampler
	Metrics                  *metrics.Metrics
	MaxBodyBytes             int64
	EventLogDir              string
	StartTime                time.Time
	SampleRatePct            int // 0 means use sampler's default from env
	AskProvider              llm.Provider
	AskRegistry              *tools.Registry
	AskMaxStepsDefault       int
	AskMaxStepsMax           int
	DashboardRefreshSec      int
	PrometheusURL            string
	GrafanaURL               string
	DedupCache               *DedupCache
	AgentKey                 string
	TrustProxy               bool
	ColdWriter               *coldstore.BatchWriter
	ColdStore                coldstore.Store
	PlanStore                *PlanStore
	GraphHotWindow           time.Duration
	OTLPEnabled              bool
	OTLPGRPCEnabled          bool
	OTLPGRPCAddr             string
	IncidentsEnabled         bool
	IncidentsPersistent      bool
	IncidentRebuildSupported bool
	Profile                  string
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
		sampler:                   cfg.Sampler,
		metrics:                   cfg.Metrics,
		maxBodyBytes:              maxBody,
		startTime:                 startTime,
		EventLogDir:               cfg.EventLogDir,
		sampleRatePct:             cfg.SampleRatePct,
		askProvider:               cfg.AskProvider,
		askRegistry:               cfg.AskRegistry,
		askMaxStepsDefault:        cfg.AskMaxStepsDefault,
		askMaxStepsMax:            cfg.AskMaxStepsMax,
		dashboardRefreshSec:       cfg.DashboardRefreshSec,
		prometheusURL:             cfg.PrometheusURL,
		grafanaURL:                cfg.GrafanaURL,
		dedupCache:                cfg.DedupCache,
		agentKey:                  cfg.AgentKey,
		trustProxy:                cfg.TrustProxy,
		coldWriter:                cfg.ColdWriter,
		coldStore:                 cfg.ColdStore,
		planStore:                 cfg.PlanStore,
		graphHotWindow:            cfg.GraphHotWindow,
		otlpEnabled:               cfg.OTLPEnabled,
		otlpGRPCEnabled:           cfg.OTLPGRPCEnabled,
		otlpGRPCAddr:              cfg.OTLPGRPCAddr,
		incidentsEnabled:          cfg.IncidentsEnabled,
		incidentsPersistent:       cfg.IncidentsPersistent,
		incidentsRebuildSupported: cfg.IncidentRebuildSupported,
		profile:                   cfg.Profile,
		replayStatus:              "none",
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
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
	if s.replayStatus == "failed" {
		status = "degraded"
	}

	resp := map[string]any{
		"status": status,
		"uptime": time.Since(s.startTime).Round(time.Second).String(),
		"ready":  s.ready.Load(),
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

// Store returns the server's graph store.

// AcceptedCount returns the number of accepted events.
func (s *Server) AcceptedCount() uint64 {
	return s.accepted.Load()
}

// Builder returns the server's graph builder.

// Sampler returns the server's sampler so external schema-1.x pipeline wiring
// can share the same sampling policy.
func (s *Server) Sampler() *sampler.Sampler { return s.sampler }

// Counters returns the shared unsampled windowed counters for schema-1.x
// pipeline wiring.
func (s *Server) Counters() *unsampledCounters { return &s.counters }

// AcceptedPtr returns a pointer to the accepted-events atomic counter so the
// shared pipeline can bump it in lockstep with the SDK Events() handler.
func (s *Server) AcceptedPtr() *atomic.Uint64 { return &s.accepted }

// SetOTLPEnabled toggles the OTLP capability flag reported by /v1/capabilities.
// Called once at startup after the OTLP route has been registered.
func (s *Server) SetOTLPEnabled(enabled bool) { s.otlpEnabled = enabled }

// SetOTLPGRPC marks the OTLP/gRPC trace receiver as mounted.
func (s *Server) SetOTLPGRPC(enabled bool, addr string) {
	s.otlpGRPCEnabled = enabled
	s.otlpGRPCAddr = addr
}

// Capabilities handles GET /v1/capabilities.
// It returns runtime capabilities/config used by UI clients.
func (s *Server) Capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	askState := s.askCapabilityState()
	hotWindow := s.effectiveGraphHotWindow()
	_, hotWindowSource := runtimeGraphHotWindow()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ask": map[string]any{
			"enabled":           askState.AskEnabled,
			"model":             askState.Model,
			"tool_mode":         askState.ToolMode,
			"max_steps_default": s.askMaxStepsDefault,
			"max_steps_max":     s.askMaxStepsMax,
		},
		"llm": map[string]any{
			"provider":    askState.Provider,
			"model":       askState.Model,
			"tool_mode":   askState.ToolMode,
			"configured":  askState.Configured,
			"ask_enabled": askState.AskEnabled,
		},
		"dashboard": map[string]any{
			"refresh_interval_sec": s.dashboardRefreshSec,
		},
		"links": map[string]any{
			"prometheus": s.prometheusURL,
			"grafana":    s.grafanaURL,
		},
		"otlp": map[string]any{
			"http_traces": s.otlpEnabled,
			"grpc_traces": s.otlpGRPCEnabled,
			"grpc_addr":   s.otlpGRPCAddr,
		},
		"profile": s.profile,
		"incidents": map[string]any{
			"enabled":    s.incidentsEnabled,
			"persistent": s.incidentsPersistent,
			"rebuild": map[string]any{
				"supported": s.incidentsRebuildSupported,
				"scope": func() string {
					if s.incidentsRebuildSupported {
						return "hot-window"
					}
					return ""
				}(),
			},
		},
		"architecture": map[string]any{
			"flattened": true,
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
		respondError(w, r, http.StatusInternalServerError, "INTERNAL", "tool registry unavailable", true, APIMeta{RequestID: RequestIDFromContext(r.Context())})
		return
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

// Ask handles POST /v1/ask and returns an LLM answer backed by the agent tools.
func (s *Server) Ask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false, APIMeta{RequestID: RequestIDFromContext(r.Context())})
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

	answer, toolRecords, askErr := llm.Ask(ctx, provider, defs, llm.ToolExecutorFunc(func(ctx context.Context, name string, params json.RawMessage) (any, error) {
		return registry.Call(ctx, name, params)
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

type askCapability struct {
	Provider   string
	Model      string
	ToolMode   string
	Configured bool
	AskEnabled bool
}

func (s *Server) askProviderFromEnv() (llm.Provider, string, string, error) {
	sel, err := llm.SelectFromEnv()
	if err != nil {
		return nil, "", "", err
	}
	if !sel.AskEnabled {
		return nil, "", "", llm.ErrProviderNotConfigured
	}
	return sel.Impl, sel.Model, sel.ToolMode, nil
}

// askCapabilityState reports current LLM provider state for /v1/capabilities.
// When s.askProvider != nil (test injection), provider is reported as "custom".
func (s *Server) askCapabilityState() askCapability {
	if s.askProvider != nil {
		return askCapability{Provider: "custom", Configured: true, AskEnabled: true}
	}
	sel, err := llm.SelectFromEnv()
	if err != nil {
		return askCapability{Provider: "none"}
	}
	return askCapability{
		Provider:   sel.Provider,
		Model:      sel.Model,
		ToolMode:   sel.ToolMode,
		Configured: sel.Configured,
		AskEnabled: sel.AskEnabled,
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

	result, err := registry.Call(r.Context(), toolName, params)
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

	var req PlanExecuteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		if dedupIsExecutor {
			dedupCompleted = true
			s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body,
				http.StatusBadRequest, nil, &APIError{Code: "BAD_REQUEST", Message: "invalid JSON: " + err.Error()}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error(), false, APIMeta{RequestID: reqID})
		return
	}

	steps, expandErr := ExpandPlanRequest(req)

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

	if expandErr != nil {
		if dedupIsExecutor {
			dedupCompleted = true
			s.dedupCache.Complete(r.Method, r.URL.Path, s.dedupPrincipal(r), idempKey, body,
				http.StatusBadRequest, nil, &APIError{Code: "INVALID_PLAN", Message: expandErr.Error()}, time.Since(start).Milliseconds())
		}
		respondError(w, r, http.StatusBadRequest, "INVALID_PLAN", expandErr.Error(), false, APIMeta{RequestID: reqID})
		return
	}

	if errs := ValidatePlan(steps, registry); len(errs) > 0 {
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

	result := s.executePlanWithProgress(r.Context(), steps, registry, planID)

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

		toolResult, err := registry.Call(ctx, step.Tool, params)
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
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
