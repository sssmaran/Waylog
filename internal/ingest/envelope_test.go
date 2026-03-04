package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIVersion(t *testing.T) {
	if apiVersion != "2026-03-02" {
		t.Errorf("apiVersion = %q, want %q", apiVersion, "2026-03-02")
	}
}

func TestWriteJSON_Envelope(t *testing.T) {
	w := httptest.NewRecorder()
	meta := APIMeta{RequestID: "req_test123", DurationMs: 42, DataStatus: "complete"}
	writeJSON(w, http.StatusOK, map[string]int{"x": 1}, meta, nil)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Waylog-API-Version"); got != apiVersion {
		t.Errorf("header = %q, want %q", got, apiVersion)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Meta.APIVersion != apiVersion {
		t.Errorf("meta.api_version = %q, want %q", resp.Meta.APIVersion, apiVersion)
	}
	if resp.Error != nil {
		t.Error("expected nil error")
	}
}

func TestWriteError_Envelope(t *testing.T) {
	w := httptest.NewRecorder()
	meta := APIMeta{RequestID: "req_err1"}
	writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "bad", false, meta)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var resp APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error == nil || resp.Error.Code != "INVALID_PARAMS" {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestWantsEnvelope_AcceptHeader(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"application/json", false},
		{"application/json;envelope=v2", true},
		{"text/html, application/json; envelope=v2", true},
		{"text/html, application/json; charset=utf-8", false},
		{"application/json; envelope=v1", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/test", nil)
		if tt.accept != "" {
			r.Header.Set("Accept", tt.accept)
		}
		if got := wantsEnvelope(r); got != tt.want {
			t.Errorf("Accept=%q: got %v, want %v", tt.accept, got, tt.want)
		}
	}
}

func TestWantsEnvelope_QueryParam(t *testing.T) {
	r := httptest.NewRequest("GET", "/test?envelope=v2", nil)
	if !wantsEnvelope(r) {
		t.Error("expected true for ?envelope=v2")
	}
}

func TestGenerateRequestID(t *testing.T) {
	id := generateRequestID()
	if !strings.HasPrefix(id, "req_") {
		t.Errorf("id %q doesn't start with req_", id)
	}
	if len(id) != 4+16 { // "req_" + 16 hex chars
		t.Errorf("id %q has wrong length %d", id, len(id))
	}

	id2 := generateRequestID()
	if id == id2 {
		t.Error("expected unique IDs")
	}
}

func TestRequestIDContext(t *testing.T) {
	ctx := ContextWithRequestID(context.Background(), "req_abc123")
	if got := RequestIDFromContext(ctx); got != "req_abc123" {
		t.Errorf("got %q, want req_abc123", got)
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
