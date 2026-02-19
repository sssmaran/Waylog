package ingest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tracestory"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// Server handles HTTP requests for the ingest service.
type Server struct {
	store        *store.Store
	builder      *build.Builder
	sampler      *sampler.Sampler
	EventLog     *eventlog.Writer
	EventLogDir  string
	accepted     atomic.Uint64
	maxBodyBytes int64
}

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Store        *store.Store
	Sampler      *sampler.Sampler
	MaxBodyBytes int64
	EventLogDir  string
}

// NewServer creates a new ingest server with the given configuration.
func NewServer(cfg ServerConfig) *Server {
	maxBody := cfg.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	s := &Server{
		store:        cfg.Store,
		builder:      build.NewBuilder(),
		sampler:      cfg.Sampler,
		maxBodyBytes: maxBody,
		EventLogDir:  cfg.EventLogDir,
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
	}
	return s
}

// Health handles health check requests.
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// Events handles event ingestion requests.
func (s *Server) Events(w http.ResponseWriter, r *http.Request) {
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
		slog.Warn("json decode failed", "err", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Ensure server-side timestamp sanity
	if ev.Timestamp.After(time.Now().Add(5 * time.Minute)) {
		http.Error(w, "timestamp too far in future", http.StatusBadRequest)
		return
	}

	if err := ev.Validate(); err != nil {
		slog.Warn("event validation failed", "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !s.sampler.ShouldKeep(ev) {
		// Dropped by design — still return 202 so producers never retry
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if s.EventLog != nil {
		if err := s.EventLog.Write(&ev); err != nil {
			slog.Error("eventlog write failed", "err", err)
		}
	}

	slog.Info("event accepted",
		"trace_id", ev.Request.TraceID,
		"status_code", ev.Outcome.StatusCode,
		"success", ev.Outcome.Success,
		"error_code", errorCode(&ev),
	)

	// Build graph from event and merge into store
	g := s.builder.Build(ev)
	if s.store != nil {
		s.store.Merge(g)
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
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Start = t
		}
	}
	if v := q.Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.End = t
		}
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

	// Count total requests and failures in window
	totalRequests := 0
	totalFailures := 0
	for _, n := range snap.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if n.LastSeen.Before(start) || n.LastSeen.After(now) {
			continue
		}
		totalRequests++
		if success, ok := n.Attr["success"].(bool); ok && !success {
			totalFailures++
		}
	}

	errorRate := 0.0
	if totalRequests > 0 {
		errorRate = float64(totalFailures) / float64(totalRequests) * 100
	}

	// Top errors sorted by count
	type errorEntry struct {
		Code  string `json:"code"`
		Count int    `json:"count"`
	}
	var topErrors []errorEntry
	for code, count := range summary.ErrorCount {
		topErrors = append(topErrors, errorEntry{Code: code, Count: count})
	}
	sort.Slice(topErrors, func(i, j int) bool {
		return topErrors[i].Count > topErrors[j].Count
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"window":         dur.String(),
		"total_requests": totalRequests,
		"total_failures": totalFailures,
		"error_rate":     errorRate,
		"top_errors":     topErrors,
		"recent_traces":  recent,
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
