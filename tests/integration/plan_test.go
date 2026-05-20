package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlan_ValidationErrors(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "empty steps",
			body: map[string]any{"steps": []map[string]any{}},
		},
		{
			name: "unknown tool",
			body: map[string]any{"steps": []map[string]any{
				{"id": "a", "tool": "nonexistent_tool", "params": map[string]any{}},
			}},
		},
		{
			name: "duplicate IDs",
			body: map[string]any{"steps": []map[string]any{
				{"id": "a", "tool": "explain_request", "params": map[string]any{"trace_id": "x"}},
				{"id": "a", "tool": "blast_radius", "params": map[string]any{}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httpPOST(t, srv.PlanExecute, "/v1/plans/execute", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPlan_SSEStream_NotFound(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/stream/plans/nonexistent-id", nil)
	rec := httptest.NewRecorder()
	srv.PlanStream(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
