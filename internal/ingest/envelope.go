package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

const apiVersion = "2026-03-02"

// APIResponse is the standard envelope for agent-facing APIs.
type APIResponse struct {
	Data  any       `json:"data"`
	Meta  APIMeta   `json:"meta"`
	Error *APIError `json:"error"` // always serialized (null when nil)
}

// APIMeta carries correlation and timing metadata.
type APIMeta struct {
	RequestID  string `json:"request_id"`
	DurationMs int64  `json:"duration_ms"`
	DataStatus string `json:"data_status"`
	APIVersion string `json:"api_version"`
	Cached     bool   `json:"cached"`
}

// APIError is the error payload inside the envelope.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// writeJSON writes a full envelope response.
func writeJSON(w http.ResponseWriter, status int, data any, meta APIMeta, apiErr *APIError) {
	meta.APIVersion = apiVersion
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Waylog-API-Version", apiVersion)
	w.Header().Set("Vary", "Accept")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIResponse{
		Data:  data,
		Meta:  meta,
		Error: apiErr,
	})
}

// writeError is a shorthand for error-only envelope responses.
func writeError(w http.ResponseWriter, status int, code, msg string, retryable bool, meta APIMeta) {
	writeJSON(w, status, nil, meta, &APIError{
		Code:      code,
		Message:   msg,
		Retryable: retryable,
	})
}

// wantsEnvelope returns true if the client opted into the v2 envelope format.
// Checks Accept header for application/json;envelope=v2, or ?envelope=v2 query param.
func wantsEnvelope(r *http.Request) bool {
	if r.URL.Query().Get("envelope") == "v2" {
		return true
	}
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	for _, token := range strings.Split(accept, ",") {
		token = strings.TrimSpace(token)
		mediaType, params, err := mime.ParseMediaType(token)
		if err != nil {
			continue
		}
		if mediaType == "application/json" && params["envelope"] == "v2" {
			return true
		}
	}
	return false
}

// generateRequestID creates a unique request ID: req_ + 16 hex chars.
func generateRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

type contextKey int

const requestIDKey contextKey = iota

// ContextWithRequestID stores a request ID in the context.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext retrieves the request ID from context.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
