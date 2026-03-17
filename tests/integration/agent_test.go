package integration

import (
	"net/http"
	"reflect"
	"testing"
)

func TestAgent_TriageWorkflow(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	// Inject failure scenario: 50 healthy + 20 PMT_502 + 10 CHK_TIMEOUT.
	ingestEvents(t, srv, makeHealthyEvents(50, "api-gateway"))
	ingestEvents(t, srv, makeFailureEvents(20, "payment-service", "PMT_502"))
	ingestEvents(t, srv, makeFailureEvents(10, "checkout-service", "CHK_TIMEOUT"))

	// Step 1: graph_insights
	iw := httpPOST(t, srv.ToolCall, "/v1/tools/graph_insights",
		map[string]string{"window": "10m"})
	if iw.Code != http.StatusOK {
		t.Fatalf("graph_insights: expected 200, got %d: %s", iw.Code, iw.Body.String())
	}
	var insights struct {
		SchemaVersion string `json:"schema_version"`
		TotalFailures int    `json:"total_failures"`
	}
	decodeJSON(t, iw, &insights)
	if insights.SchemaVersion != "1.0" {
		t.Errorf("expected schema_version 1.0, got %q", insights.SchemaVersion)
	}
	if insights.TotalFailures < 20 {
		t.Errorf("expected >= 20 failures, got %d", insights.TotalFailures)
	}

	// Step 2: failure_patterns with pagination
	pw := httpPOST(t, srv.ToolCall, "/v1/tools/failure_patterns",
		map[string]any{"window": "10m", "limit": 5})
	if pw.Code != http.StatusOK {
		t.Fatalf("failure_patterns: expected 200, got %d: %s", pw.Code, pw.Body.String())
	}
	var patterns struct {
		Patterns []struct {
			ErrorCode string `json:"error_code"`
			Count     int    `json:"count"`
		} `json:"patterns"`
		TotalCount int  `json:"total_count"`
		HasMore    bool `json:"has_more"`
	}
	decodeJSON(t, pw, &patterns)
	if len(patterns.Patterns) == 0 {
		t.Fatal("expected at least one failure pattern")
	}
	foundPMT := false
	for _, p := range patterns.Patterns {
		if p.ErrorCode == "PMT_502" {
			foundPMT = true
			if p.Count < 20 {
				t.Errorf("expected PMT_502 count >= 20, got %d", p.Count)
			}
		}
	}
	if !foundPMT {
		t.Error("PMT_502 not found in failure patterns")
	}

	// Step 3: blast_radius for PMT_502
	bw := httpPOST(t, srv.ToolCall, "/v1/tools/blast_radius",
		map[string]any{"error_code": "PMT_502", "window": "10m", "include_services": true})
	if bw.Code != http.StatusOK {
		t.Fatalf("blast_radius: expected 200, got %d: %s", bw.Code, bw.Body.String())
	}
	var blast struct {
		AffectedRequests int `json:"affected_requests"`
		AffectedUsers    int `json:"affected_users"`
	}
	decodeJSON(t, bw, &blast)
	if blast.AffectedRequests < 20 {
		t.Errorf("expected >= 20 affected requests, got %d", blast.AffectedRequests)
	}
}

func TestAgent_IdempotencyReplay(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeFailureEvents(10, "payment-service", "PMT_502"))

	body := map[string]string{"window": "10m"}
	headers := map[string]string{"Idempotency-Key": "test-key-001"}

	// First call — executes.
	w1 := httpPOSTWithHeaders(t, srv.ToolCall, "/v1/tools/graph_insights", body, headers)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	// Second call — same key+body → cached replay.
	w2 := httpPOSTWithHeaders(t, srv.ToolCall, "/v1/tools/graph_insights", body, headers)
	if w2.Code != http.StatusOK {
		t.Fatalf("replay call: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	// Verify same response content (compare decoded maps to handle key order differences).
	var r1, r2 map[string]any
	decodeJSON(t, w1, &r1)
	decodeJSON(t, w2, &r2)
	if !reflect.DeepEqual(r1, r2) {
		t.Errorf("replay response differs:\n  first:  %v\n  replay: %v", r1, r2)
	}
}

func TestAgent_IdempotencyConflict(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)
	ingestEvents(t, srv, makeFailureEvents(10, "payment-service", "PMT_502"))

	headers := map[string]string{"Idempotency-Key": "test-key-002"}

	// First call.
	w1 := httpPOSTWithHeaders(t, srv.ToolCall, "/v1/tools/graph_insights",
		map[string]string{"window": "10m"}, headers)
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d", w1.Code)
	}

	// Same key, different body → 409.
	w2 := httpPOSTWithHeaders(t, srv.ToolCall, "/v1/tools/graph_insights",
		map[string]string{"window": "5m"}, headers)
	if w2.Code != http.StatusConflict {
		t.Fatalf("conflict call: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestAgent_ToolNotFound(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	w := httpPOST(t, srv.ToolCall, "/v1/tools/nonexistent_tool", map[string]string{})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
