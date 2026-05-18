package microdemo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeProofRejectsNonPOST(t *testing.T) {
	gateway := NewGatewayHandler("http://checkout.example")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/demo/proof", nil)
	gateway.ServeProof(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServeProofRequiresIngestURL(t *testing.T) {
	gateway := NewGatewayHandler("http://checkout.example")
	gateway.SetPurchaseHandler(okBurstDispatch())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/demo/proof", strings.NewReader(`{}`))
	gateway.ServeProof(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestServeProofRejectsUnknownFields(t *testing.T) {
	gateway := NewGatewayHandler("http://checkout.example")
	gateway.SetWaylogAPI("http://ingest.example", "read", "write", "agent")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/demo/proof", strings.NewReader(`{"foo":1}`))
	gateway.ServeProof(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestProofSummaryJSONShape(t *testing.T) {
	out := ProofSummary{
		AlertID:    "alert_1",
		IncidentID: "inc_1",
		ReportHash: "sha256:x",
		Hashes:     map[string]string{"read": "sha256:x"},
		Evidence:   ProofEvidence{TraceID: "trace_1", AlertLinked: true, DependencySignal: true, NextChecks: true},
		Scorecard: ProofScorecard{
			RootCauseAccuracy:             true,
			CauseClassificationDependency: true,
			ReportHashStable:              true,
			TriageLatencyMS:               42,
			Scenario:                      "warm-demo",
		},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"alert_1", "inc_1", "trace_1", "cause_classification_dependency", `"triage_latency_ms":42`, `"scenario":"warm-demo"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json missing %q: %s", want, raw)
		}
	}
}

func TestProofSummaryReportHashStableFalse(t *testing.T) {
	out := ProofSummary{
		AlertID:    "alert_1",
		IncidentID: "inc_1",
		ReportHash: "sha256:x",
		Hashes:     map[string]string{"read": "sha256:x", "repeat": "sha256:y"},
		Scorecard: ProofScorecard{
			ReportHashStable: false,
			TriageLatencyMS:  7,
			Scenario:         "warm-demo",
		},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"report_hash_stable":false`) {
		t.Fatalf("json missing report_hash_stable:false: %s", raw)
	}
}
