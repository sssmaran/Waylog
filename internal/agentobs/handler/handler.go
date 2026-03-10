package handler

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/metrics"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
)

type CostRate struct {
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
}

type HandlerConfig struct {
	APIKey         string
	RedactPayloads bool
	CostRates      map[string]CostRate
}

type Handler struct {
	store   *store.Store
	wal     *eventlog.Writer
	metrics *metrics.Metrics
	config  HandlerConfig
	dedup   map[string]bool
	dedupMu sync.RWMutex
}

func NewHandler(s *store.Store, w *eventlog.Writer, m *metrics.Metrics, cfg HandlerConfig) *Handler {
	return &Handler{
		store:   s,
		wal:     w,
		metrics: m,
		config:  cfg,
		dedup:   make(map[string]bool),
	}
}

func (h *Handler) SetDedupIndex(idx map[string]bool) {
	h.dedupMu.Lock()
	defer h.dedupMu.Unlock()
	h.dedup = idx
}

type IngestRequest struct {
	Events []agentobs.AgentEvent `json:"events"`
}

type IngestResponse struct {
	Accepted   int          `json:"accepted"`
	Duplicated int          `json:"duplicated"`
	Rejected   int          `json:"rejected"`
	Errors     []EventError `json:"errors"`
}

type EventError struct {
	EventID string `json:"event_id"`
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.config.APIKey != "" && !h.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	start := time.Now()
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	resp := IngestResponse{}
	for i, ev := range req.Events {
		if err := ev.Validate(); err != nil {
			resp.Rejected++
			resp.Errors = append(resp.Errors, EventError{
				EventID: ev.EventID, Index: i,
				Code: "INVALID_SCHEMA", Message: err.Error(),
			})
			continue
		}

		h.dedupMu.Lock()
		if h.dedup[ev.EventID] {
			h.dedupMu.Unlock()
			resp.Duplicated++
			continue
		}
		h.dedup[ev.EventID] = true
		h.dedupMu.Unlock()

		if h.wal != nil {
			if err := h.wal.Write(&ev); err != nil {
				slog.Error("wal_write_failed", "event_id", ev.EventID, "err", err)
				h.dedupMu.Lock()
				delete(h.dedup, ev.EventID)
				h.dedupMu.Unlock()
				resp.Rejected++
				resp.Errors = append(resp.Errors, EventError{
					EventID: ev.EventID, Index: i,
					Code: "WAL_FAILURE", Message: "durable write failed",
				})
				continue
			}
		}

		if err := h.store.Merge(&ev); err != nil {
			h.dedupMu.Lock()
			delete(h.dedup, ev.EventID)
			h.dedupMu.Unlock()
			resp.Rejected++
			resp.Errors = append(resp.Errors, EventError{
				EventID: ev.EventID, Index: i,
				Code: err.Error(), Message: err.Error(),
			})
			continue
		}

		resp.Accepted++
	}

	if h.metrics != nil {
		h.metrics.IngestDuration.Observe(time.Since(start).Seconds())
		h.metrics.IngestEventsTotal.WithLabelValues("accepted").Add(float64(resp.Accepted))
		h.metrics.IngestEventsTotal.WithLabelValues("duplicated").Add(float64(resp.Duplicated))
		h.metrics.IngestEventsTotal.WithLabelValues("rejected").Add(float64(resp.Rejected))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		return subtle.ConstantTimeCompare([]byte(token), []byte(h.config.APIKey)) == 1
	}
	key := r.Header.Get("X-API-Key")
	if key != "" {
		return subtle.ConstantTimeCompare([]byte(key), []byte(h.config.APIKey)) == 1
	}
	return false
}

func (h *Handler) Livez(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"runs":     h.store.RunCount(),
		"sessions": h.store.SessionCount(),
	})
}
