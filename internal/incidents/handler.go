package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	writeJSON(w, http.StatusOK, ActiveResponse{Incidents: rows})
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
	writeJSON(w, http.StatusOK, DetailResponse{Incident: inc})
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
		writeJSON(w, http.StatusOK, SnapshotResponse{Snapshot: snapshot, Incident: inc})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(snapshot))
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
