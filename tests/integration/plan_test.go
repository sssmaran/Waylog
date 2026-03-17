package integration

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlan_HappyPath(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeFailureEvents(20, "payment-service", "PMT_502"))

	body := map[string]any{
		"steps": []map[string]any{
			{"id": "insights", "tool": "graph_insights", "params": map[string]any{"window": "10m"}},
			{"id": "patterns", "tool": "failure_patterns", "params": map[string]any{"window": "10m", "limit": 5}},
		},
	}
	w := httpPOST(t, srv.PlanExecute, "/v1/plans/execute", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		PlanID    string `json:"plan_id"`
		Status    string `json:"status"`
		Completed int    `json:"completed"`
		Total     int    `json:"total"`
		Steps     []struct {
			ID   string `json:"id"`
			Tool string `json:"tool"`
		} `json:"steps"`
	}
	decodeJSON(t, w, &result)

	if result.Status != "complete" {
		t.Errorf("status = %q, want complete", result.Status)
	}
	if result.Completed != 2 {
		t.Errorf("completed = %d, want 2", result.Completed)
	}
	if result.Total != 2 {
		t.Errorf("total = %d, want 2", result.Total)
	}
	if result.PlanID == "" {
		t.Error("plan_id should not be empty")
	}
	if w.Header().Get("X-Plan-ID") == "" {
		t.Error("X-Plan-ID header should be set")
	}
}

func TestPlan_RefChain(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeFailureEvents(10, "payment-service", "PMT_502"))

	body := map[string]any{
		"steps": []map[string]any{
			{"id": "list_failures", "tool": "graph_failures", "params": map[string]any{"limit": 1}},
			{"id": "explain", "tool": "explain_request", "params": map[string]any{
				"trace_id": `$steps["list_failures"].result.failures[0].trace_id`,
			}},
		},
	}
	w := httpPOST(t, srv.PlanExecute, "/v1/plans/execute", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		Status    string `json:"status"`
		Completed int    `json:"completed"`
		Steps     []struct {
			ID    string `json:"id"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"steps"`
	}
	decodeJSON(t, w, &result)

	if result.Completed != 2 {
		t.Errorf("completed = %d, want 2", result.Completed)
	}
	if result.Status != "complete" {
		t.Errorf("status = %q, want complete", result.Status)
	}
	for _, step := range result.Steps {
		if step.Error != nil {
			t.Errorf("step %q has error: %s", step.ID, step.Error.Code)
		}
	}
}

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
				{"id": "a", "tool": "graph_insights", "params": map[string]any{"window": "10m"}},
				{"id": "a", "tool": "graph_failures", "params": map[string]any{}},
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

func TestPlan_Idempotency(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeHealthyEvents(5, "api-gateway"))

	body := map[string]any{
		"steps": []map[string]any{
			{"id": "insights", "tool": "graph_insights", "params": map[string]any{"window": "10m"}},
		},
	}

	// First call
	w1 := httpPOSTWithHeaders(t, srv.PlanExecute, "/v1/plans/execute", body, map[string]string{
		"Idempotency-Key": "plan-test-1",
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	// Replay with same key
	w2 := httpPOSTWithHeaders(t, srv.PlanExecute, "/v1/plans/execute", body, map[string]string{
		"Idempotency-Key": "plan-test-1",
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	// Conflict: same key, different body
	differentBody := map[string]any{
		"steps": []map[string]any{
			{"id": "insights", "tool": "graph_insights", "params": map[string]any{"window": "5m"}},
		},
	}
	w3 := httpPOSTWithHeaders(t, srv.PlanExecute, "/v1/plans/execute", differentBody, map[string]string{
		"Idempotency-Key": "plan-test-1",
	})
	if w3.Code != http.StatusConflict {
		t.Errorf("conflict: expected 409, got %d: %s", w3.Code, w3.Body.String())
	}
}

func TestPlan_SSEStream(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeFailureEvents(10, "payment-service", "PMT_502"))

	// Execute a plan first to get a plan ID
	body := map[string]any{
		"steps": []map[string]any{
			{"id": "insights", "tool": "graph_insights", "params": map[string]any{"window": "10m"}},
		},
	}
	w := httpPOST(t, srv.PlanExecute, "/v1/plans/execute", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	planID := w.Header().Get("X-Plan-ID")
	if planID == "" {
		t.Fatal("no X-Plan-ID header")
	}

	// Subscribe to the completed plan — should get replay
	req := httptest.NewRequest(http.MethodGet, "/v1/stream/plans/"+planID, nil)
	rec := httptest.NewRecorder()
	srv.PlanStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SSE: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Parse SSE events
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	var eventTypes []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventTypes = append(eventTypes, strings.TrimPrefix(line, "event: "))
		}
	}

	// Should have step_start, step_complete, done
	if len(eventTypes) < 3 {
		t.Errorf("expected at least 3 SSE events, got %d: %v", len(eventTypes), eventTypes)
	}

	// Last event should be "done"
	if len(eventTypes) > 0 && eventTypes[len(eventTypes)-1] != "done" {
		t.Errorf("last event should be 'done', got %q", eventTypes[len(eventTypes)-1])
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

