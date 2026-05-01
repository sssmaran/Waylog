package microdemo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPickBurstScenarioFloatBoundaries(t *testing.T) {
	tests := []struct {
		x    float64
		want string
	}{
		{0.00, ScenarioHappy},
		{0.699, ScenarioHappy},
		{0.70, ScenarioPayment502},
		{0.849, ScenarioPayment502},
		{0.85, ScenarioDBMiss},
		{0.929, ScenarioDBMiss},
		{0.93, ScenarioCheckoutError},
		{0.979, ScenarioCheckoutError},
		{0.98, ScenarioSuppressedPayment502},
		{0.999, ScenarioSuppressedPayment502},
		{1.0, ScenarioSuppressedPayment502},
	}
	for _, tt := range tests {
		if got := pickBurstScenarioFloat(tt.x); got != tt.want {
			t.Fatalf("pickBurstScenarioFloat(%v) = %q, want %q", tt.x, got, tt.want)
		}
	}
}

func TestPickBurstScenarioFloatAllScenariosReachable(t *testing.T) {
	seen := map[string]bool{}
	for _, x := range []float64{0.1, 0.75, 0.88, 0.95, 0.99} {
		seen[pickBurstScenarioFloat(x)] = true
	}
	for _, scenario := range []string{
		ScenarioHappy,
		ScenarioPayment502,
		ScenarioDBMiss,
		ScenarioCheckoutError,
		ScenarioSuppressedPayment502,
	} {
		if !seen[scenario] {
			t.Fatalf("scenario %q was not reachable; seen=%v", scenario, seen)
		}
	}
}

func TestRunBurstDispatchesEveryRequestThroughHandler(t *testing.T) {
	var calls atomic.Int64
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"trace_id":"t","scenario":"happy"}`))
	})

	summary := runBurst(t.Context(), dispatch, BurstRequest{Requests: 20, Concurrency: 4})
	if calls.Load() != 20 {
		t.Fatalf("dispatch calls = %d, want 20", calls.Load())
	}
	if summary.Accepted.Requests != 20 || summary.Accepted.Concurrency != 4 {
		t.Fatalf("accepted = %#v, want 20/4", summary.Accepted)
	}
	if summary.OK != 20 || summary.Errors != 0 || summary.Suppressed != 0 {
		t.Fatalf("summary counts = ok:%d errors:%d suppressed:%d", summary.OK, summary.Errors, summary.Suppressed)
	}
	if summary.ByScenario[ScenarioHappy] != 20 {
		t.Fatalf("happy count = %d, want 20", summary.ByScenario[ScenarioHappy])
	}
}

func TestServeBurstRejectsNonPOST(t *testing.T) {
	gateway := NewGatewayHandler("http://checkout.example")
	gateway.SetPurchaseHandler(okBurstDispatch())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/demo/burst", nil)
	gateway.ServeBurst(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestServeBurstRejectsUnknownFields(t *testing.T) {
	rec := serveBurstForTest(t, `{"foo":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeBurstRejectsMalformedJSON(t *testing.T) {
	rec := serveBurstForTest(t, `garbage`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestServeBurstClampsLimitsAndEchoesRequested(t *testing.T) {
	rec := serveBurstForTest(t, `{"requests":9999,"concurrency":9999}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var summary BurstSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Requested.Requests != 9999 || summary.Requested.Concurrency != 9999 {
		t.Fatalf("requested = %#v, want 9999/9999", summary.Requested)
	}
	if summary.Accepted.Requests != maxBurstRequests || summary.Accepted.Concurrency != maxBurstConcurrency {
		t.Fatalf("accepted = %#v, want %d/%d", summary.Accepted, maxBurstRequests, maxBurstConcurrency)
	}
}

func TestServeBurstAppliesDefaultsWhenZero(t *testing.T) {
	rec := serveBurstForTest(t, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var summary BurstSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Requested.Requests != defaultBurstRequests || summary.Requested.Concurrency != defaultBurstConcurrency {
		t.Fatalf("requested defaults = %#v, want %d/%d", summary.Requested, defaultBurstRequests, defaultBurstConcurrency)
	}
	if summary.Accepted.Requests != defaultBurstRequests || summary.Accepted.Concurrency != defaultBurstConcurrency {
		t.Fatalf("accepted defaults = %#v, want %d/%d", summary.Accepted, defaultBurstRequests, defaultBurstConcurrency)
	}
}

func serveBurstForTest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gateway := NewGatewayHandler("http://checkout.example")
	gateway.SetPurchaseHandler(okBurstDispatch())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/demo/burst", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	gateway.ServeBurst(rec, req)
	return rec
}

func okBurstDispatch() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req PurchaseRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":  req.Scenario == ScenarioHappy,
			"trace_id": "trace-" + req.Scenario,
			"scenario": req.Scenario,
		})
	})
}
