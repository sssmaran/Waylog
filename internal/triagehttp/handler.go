package triagehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/reports"
	"github.com/sssmaran/WaylogCLI/internal/triage"
)

type Handler struct {
	engine *triage.Engine
}

func NewHandler(engine *triage.Engine) *Handler {
	return &Handler{engine: engine}
}

func (h *Handler) Triage(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/report") {
		h.Report(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	id := incidentIDFromPath(r.URL.Path)
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_incident_id", "incident_id required in path", "")
		return
	}
	q := r.URL.Query()
	opts, err := triage.ParseBuildOptions(q.Get("window"), q.Get("snapshot") == "true", time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_options", err.Error(), "")
		return
	}
	rep, err := h.engine.Build(r.Context(), id, opts)
	if errors.Is(err, triage.ErrUnknownIncident) {
		writeError(w, http.StatusNotFound, "not_found", "incident not found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "triage_build_failed", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	id := incidentIDFromPath(strings.TrimSuffix(strings.TrimRight(r.URL.Path, "/"), "/report"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing_incident_id", "incident_id required in path", "")
		return
	}
	q := r.URL.Query()
	opts, err := triage.ParseBuildOptions(q.Get("window"), q.Get("snapshot") == "true", time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_options", err.Error(), "")
		return
	}
	rep, err := h.engine.Build(r.Context(), id, opts)
	if errors.Is(err, triage.ErrUnknownIncident) {
		writeError(w, http.StatusNotFound, "not_found", "incident not found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "triage_build_failed", err.Error(), "")
		return
	}
	rendered, err := reports.Render(rep, q.Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_format", err.Error(), "")
		return
	}
	body, err := reports.EncodeBody(rendered)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render_failed", err.Error(), "")
		return
	}
	w.Header().Set("Content-Type", rendered.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func incidentIDFromPath(path string) string {
	id := strings.TrimPrefix(path, "/v1/triage/")
	id = strings.Trim(id, "/")
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
			"detail":  detail,
		},
	})
}
