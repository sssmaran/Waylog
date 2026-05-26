package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
		Propagation:             toAPIPropagation(inc.Propagation),
		Blast:                   toAPIBlast(inc.Blast),
		Alerts:                  toAPIAlerts(inc.Alerts),
	}
}

func toAPIPropagation(s *PropagationSnapshot) *apiv2.PropagationSnapshot {
	if s == nil {
		return nil
	}
	return &apiv2.PropagationSnapshot{
		Opening: toAPIPropagationEvidence(s.Opening),
		Latest:  toAPIPropagationEvidence(s.Latest),
	}
}

func toAPIPropagationEvidence(p *PropagationEvidence) *apiv2.PropagationEvidence {
	if p == nil {
		return nil
	}
	path := make([]apiv2.PropagationStep, 0, len(p.Path))
	for _, s := range p.Path {
		path = append(path, apiv2.PropagationStep{
			Service:    s.Service,
			Step:       s.Step,
			StartMS:    s.StartMS,
			DurationMS: s.DurationMS,
			Status:     s.Status,
			ErrorCode:  s.ErrorCode,
		})
	}
	var firstSeen *time.Time
	if p.FirstSeenAt != nil {
		t := *p.FirstSeenAt
		firstSeen = &t
	}
	return &apiv2.PropagationEvidence{
		OriginService: p.OriginService,
		OriginStep:    p.OriginStep,
		Path:          path,
		SampleTraceID: p.SampleTraceID,
		FirstSeenAt:   firstSeen,
		CapturedAt:    p.CapturedAt,
		CaptureStatus: string(p.CaptureStatus),
	}
}

func toAPIBlast(s *BlastSnapshot) *apiv2.BlastSnapshot {
	if s == nil {
		return nil
	}
	return &apiv2.BlastSnapshot{
		Opening: toAPIBlastEvidence(s.Opening),
		Latest:  toAPIBlastEvidence(s.Latest),
	}
}

func toAPIBlastEvidence(b *BlastEvidence) *apiv2.BlastEvidence {
	if b == nil {
		return nil
	}
	var users *int
	if b.AffectedUsers != nil {
		u := *b.AffectedUsers
		users = &u
	}
	return &apiv2.BlastEvidence{
		AffectedRequests: b.AffectedRequests,
		AffectedUsers:    users,
		AffectedServices: b.AffectedServices,
		TopServices:      append([]string(nil), b.TopServices...),
		SampledTraces:    append([]string(nil), b.SampledTraces...),
		CapturedAt:       b.CapturedAt,
		CaptureStatus:    string(b.CaptureStatus),
	}
}

func toAPIAlerts(s *AlertSnapshot) *apiv2.AlertSnapshot {
	if s == nil {
		return nil
	}
	return &apiv2.AlertSnapshot{
		Opening: toAPIAlertEvidence(s.Opening),
		Latest:  toAPIAlertEvidence(s.Latest),
	}
}

func toAPIAlertEvidence(a *AlertEvidence) *apiv2.AlertEvidence {
	if a == nil {
		return nil
	}
	matches := make([]apiv2.MatchedAlert, 0, len(a.Matches))
	for _, m := range a.Matches {
		matches = append(matches, apiv2.MatchedAlert{
			SignalID:    m.SignalID,
			AlertID:     m.AlertID,
			Source:      m.Source,
			Severity:    m.Severity,
			Reason:      m.Reason,
			ProviderURL: m.ProviderURL,
			EvidenceIDs: append([]string(nil), m.EvidenceIDs...),
			MatchedAt:   m.MatchedAt,
			Strategy:    m.Strategy,
		})
	}
	return &apiv2.AlertEvidence{
		Matches:       matches,
		CapturedAt:    a.CapturedAt,
		CaptureStatus: string(a.CaptureStatus),
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
