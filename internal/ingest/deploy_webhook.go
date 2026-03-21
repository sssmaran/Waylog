package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
)

type deployRequest struct {
	ID       string            `json:"id"`
	Service  string            `json:"service"`
	Version  string            `json:"version"`
	Env      string            `json:"env"`
	Metadata map[string]string `json:"metadata"`
}

type deployResponse struct {
	ID              string            `json:"id"`
	Service         string            `json:"service"`
	Version         string            `json:"version,omitempty"`
	Env             string            `json:"env"`
	FirstSeen       time.Time         `json:"first_seen"`
	LastSeen        time.Time         `json:"last_seen"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ErrorRateChange *float64          `json:"error_rate_change"`
	BeforeErrorRate *float64          `json:"before_error_rate"`
	AfterErrorRate  *float64          `json:"after_error_rate"`
	BeforeRequests  int               `json:"before_requests"`
	AfterRequests   int               `json:"after_requests"`
}

// DeployRoute dispatches POST/GET/OPTIONS for /v1/deployments.
func (s *Server) DeployRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.DeployWebhook(w, r)
	case http.MethodGet:
		s.Deployments(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// DeployWebhook handles POST /v1/deployments.
func (s *Server) DeployWebhook(w http.ResponseWriter, r *http.Request) {
	meta := APIMeta{RequestID: RequestIDFromContext(r.Context())}

	if s.coldStore == nil {
		respondError(w, r, http.StatusServiceUnavailable, "COLD_STORE_UNAVAILABLE", "cold store not configured", false, meta)
		return
	}

	maxBody := s.maxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid json", false, meta)
		return
	}

	if req.ID == "" || req.Service == "" || req.Env == "" {
		respondError(w, r, http.StatusBadRequest, "MISSING_FIELDS", "id, service, and env are required", false, meta)
		return
	}

	now := time.Now().UTC()
	dep := coldstore.Deployment{
		ID:        req.ID,
		Service:   req.Service,
		Version:   req.Version,
		Env:       req.Env,
		FirstSeen: now,
		LastSeen:  now,
		Metadata:  req.Metadata,
	}

	if err := s.coldStore.UpsertDeployment(r.Context(), dep); err != nil {
		if errors.Is(err, coldstore.ErrEnvConflict) {
			respondError(w, r, http.StatusConflict, "ENV_CONFLICT", "deployment env conflict", false, meta)
			return
		}
		if s.metrics != nil {
			s.metrics.DeployUpsertErrors.Inc()
		}
		respondError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to upsert deployment", true, meta)
		return
	}

	if s.metrics != nil {
		s.metrics.DeployUpsertsTotal.Inc()
	}

	// Publish SSE event so dashboard clients see the new deployment immediately.
	if s.sseHub != nil {
		data := s.ComputeSSETopic(TopicDeployments)
		if data != nil {
			s.sseHub.Publish(TopicDeployments, data)
		}
	}

	if wantsEnvelope(r) {
		writeJSON(w, http.StatusCreated, map[string]string{"id": req.ID}, meta, nil)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": req.ID})
	}
}

// deploymentsPayload computes the deployment list with error-rate enrichment.
// Shared between the REST handler and SSE compute path.
func (s *Server) deploymentsPayload(ctx context.Context, start, now time.Time, service string) ([]deployResponse, error) {
	deps, err := s.coldStore.DeploymentsInWindow(ctx, start, now, service)
	if err != nil {
		return nil, err
	}

	const sampleWindow = 5 * time.Minute
	const minRequests = 10

	out := make([]deployResponse, 0, len(deps))
	for _, d := range deps {
		resp := deployResponse{
			ID:        d.ID,
			Service:   d.Service,
			Version:   d.Version,
			Env:       d.Env,
			FirstSeen: d.FirstSeen,
			LastSeen:  d.LastSeen,
			Metadata:  d.Metadata,
		}

		beforeRate, berr := s.coldStore.ServiceErrorRateInWindow(ctx, d.Service, d.FirstSeen.Add(-sampleWindow), d.FirstSeen)
		afterRate, aerr := s.coldStore.ServiceErrorRateInWindow(ctx, d.Service, d.FirstSeen, d.FirstSeen.Add(sampleWindow))

		resp.BeforeRequests = beforeRate.Total
		resp.AfterRequests = afterRate.Total

		if berr == nil && aerr == nil {
			bRate := float64(beforeRate.Failures) / math.Max(float64(beforeRate.Total), 1)
			aRate := float64(afterRate.Failures) / math.Max(float64(afterRate.Total), 1)

			resp.BeforeErrorRate = &bRate
			resp.AfterErrorRate = &aRate

			if beforeRate.Total >= minRequests && afterRate.Total >= minRequests {
				if beforeRate.Failures == 0 && afterRate.Failures == 0 {
					// both error rates zero: no change signal
				} else if beforeRate.Failures == 0 && afterRate.Failures > 0 {
					// new errors appeared with no baseline: skip ratio
				} else {
					change := aRate / math.Max(bRate, 0.001)
					resp.ErrorRateChange = &change
				}
			}
		}

		out = append(out, resp)
	}
	return out, nil
}

// Deployments handles GET /v1/deployments.
func (s *Server) Deployments(w http.ResponseWriter, r *http.Request) {
	meta := APIMeta{RequestID: RequestIDFromContext(r.Context())}

	if s.coldStore == nil {
		respondError(w, r, http.StatusServiceUnavailable, "COLD_STORE_UNAVAILABLE", "cold store not configured", false, meta)
		return
	}

	q := r.URL.Query()
	window := parseLooseDuration(q, "window", time.Hour)
	if window > 24*time.Hour {
		window = 24 * time.Hour
	}
	service := q.Get("service")

	now := time.Now().UTC()
	start := now.Add(-window)

	out, err := s.deploymentsPayload(r.Context(), start, now, service)
	if err != nil {
		respondError(w, r, http.StatusInternalServerError, "QUERY_FAILED", "failed to query deployments", true, meta)
		return
	}

	payload := map[string]any{"deployments": out}
	if wantsEnvelope(r) {
		writeJSON(w, http.StatusOK, payload, meta, nil)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload)
	}
}
