package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

type Handler struct {
	engine *Engine
}

func NewHandler(engine *Engine) *Handler {
	return &Handler{engine: engine}
}

func (h *Handler) Active(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	rows, err := h.engine.Active(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "query incidents failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiv2.IncidentListResponse{Incidents: toAPIIncidents(rows)})
}

func (h *Handler) Incident(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/incidents/")
	if path == "" || path == r.URL.Path {
		writeError(w, http.StatusNotFound, "not_found", "incident not found", "")
		return
	}
	if strings.HasSuffix(path, "/snapshot") {
		id := strings.TrimSuffix(path, "/snapshot")
		h.snapshot(w, r, id)
		return
	}
	inc, err := h.engine.Get(r.Context(), path)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "incident not found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "query incident failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, apiv2.IncidentDetailResponse{Incident: toAPIIncident(inc)})
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request, id string) {
	inc, err := h.engine.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "incident not found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "query incident failed", err.Error())
		return
	}
	snapshot := RenderSnapshot(inc)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, apiv2.IncidentSnapshotResponse{Snapshot: snapshot, Incident: toAPIIncident(inc)})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(snapshot))
}

func toAPIIncidents(rows []Incident) []apiv2.Incident {
	out := make([]apiv2.Incident, 0, len(rows))
	for _, inc := range rows {
		out = append(out, toAPIIncident(inc))
	}
	return out
}

func toAPIIncident(inc Incident) apiv2.Incident {
	return apiv2.Incident{
		IncidentID:              inc.IncidentID,
		Env:                     inc.Env,
		Service:                 inc.Service,
		ErrorFamily:             inc.ErrorFamily,
		Status:                  string(inc.Status),
		Cause:                   string(inc.Cause),
		Confidence:              string(inc.Confidence),
		Severity:                inc.Severity,
		StartedAt:               inc.StartedAt,
		UpdatedAt:               inc.UpdatedAt,
		LastSeenAt:              inc.LastSeenAt,
		RecoveringAt:            inc.RecoveringAt,
		ResolvedAt:              inc.ResolvedAt,
		AffectedRequests:        inc.AffectedRequests,
		AffectedUsers:           inc.AffectedUsers,
		AffectedServices:        inc.AffectedServices,
		TopServices:             inc.TopServices,
		SampleTraces:            inc.SampleTraces,
		Evidence:                toAPIEvidence(inc.Evidence),
		NextChecks:              inc.NextChecks,
		InstrumentationWarnings: inc.InstrumentationWarnings,
		Lift:                    inc.Lift,
		BaselineCount:           inc.BaselineCount,
		CurrentCount:            inc.CurrentCount,
	}
}

func toAPIEvidence(rows []Evidence) []apiv2.IncidentEvidence {
	out := make([]apiv2.IncidentEvidence, 0, len(rows))
	for _, ev := range rows {
		out = append(out, apiv2.IncidentEvidence{
			Kind:       string(ev.Kind),
			Title:      ev.Title,
			Detail:     ev.Detail,
			Service:    ev.Service,
			SignalID:   ev.SignalID,
			DeployID:   ev.DeployID,
			TraceID:    ev.TraceID,
			OccurredAt: ev.OccurredAt,
			Fields:     ev.Fields,
		})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message, detail string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"detail":  detail,
		},
	})
}
