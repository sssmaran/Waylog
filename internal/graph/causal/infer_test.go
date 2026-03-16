package causal

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// baseTime is the anchor for all test timestamps.
var baseTime = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

// buildGraph constructs a core.Graph with the given request→error edges.
// Each entry in failures is (reqID, service, errorCode, reqTime).
func buildGraph(failures []struct {
	reqID     string
	service   string
	errorCode string
	reqTime   time.Time
}) *core.Graph {
	g := core.New()
	for i, f := range failures {
		errID := f.reqID + "-err"
		_ = i

		g.AddNode(core.Node{
			ID:   f.reqID,
			Type: core.NodeRequest,
			Attr: map[string]any{
				"root_service": f.service,
			},
			FirstSeen: f.reqTime,
			LastSeen:  f.reqTime,
		})
		g.AddNode(core.Node{
			ID:   errID,
			Type: core.NodeError,
			Attr: map[string]any{
				"code": f.errorCode,
			},
			FirstSeen: f.reqTime,
			LastSeen:  f.reqTime,
		})
		g.AddEdge(core.Edge{
			From: f.reqID,
			To:   errID,
			Type: core.EdgeFailedWith,
		})
	}
	return g
}

// TestInferIntroducedBy_BasicClaim verifies that a deploy followed by a spike
// of 30+ failures (0 before) produces a single claim with the right fields.
func TestInferIntroducedBy_BasicClaim(t *testing.T) {
	deployTime := baseTime.Add(10 * time.Minute)
	start := baseTime
	end := baseTime.Add(60 * time.Minute)

	// Build 30 failures that occurred 5 minutes after the deploy.
	var failures []struct {
		reqID     string
		service   string
		errorCode string
		reqTime   time.Time
	}
	failTime := deployTime.Add(5 * time.Minute)
	for i := 0; i < 30; i++ {
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{
			reqID:     "req-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			service:   "checkout",
			errorCode: "PMT_502",
			reqTime:   failTime.Add(time.Duration(i) * time.Second),
		})
	}

	g := buildGraph(failures)
	deps := []DeploymentInfo{
		{ID: "deploy-1", Service: "checkout", FirstSeen: deployTime},
	}

	claims := InferIntroducedBy(g, deps, start, end)

	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	c := claims[0]
	if c.ClaimType != ClaimIntroducedBy {
		t.Errorf("ClaimType = %q, want %q", c.ClaimType, ClaimIntroducedBy)
	}
	if c.Subject != "PMT_502" {
		t.Errorf("Subject = %q, want PMT_502", c.Subject)
	}
	if c.Target != "deploy-1" {
		t.Errorf("Target = %q, want deploy-1", c.Target)
	}
	if c.Service != "checkout" {
		t.Errorf("Service = %q, want checkout", c.Service)
	}
	if !c.ShadowMode {
		t.Error("ShadowMode should be true")
	}
	if c.Evidence.AfterFailures != 30 {
		t.Errorf("AfterFailures = %d, want 30", c.Evidence.AfterFailures)
	}
	if c.Evidence.BeforeFailures != 0 {
		t.Errorf("BeforeFailures = %d, want 0", c.Evidence.BeforeFailures)
	}
	// With 0 before → Laplace-smoothed lift = 30/0.5 = 60.
	if c.Evidence.Lift < minLift {
		t.Errorf("Lift = %.2f, want >= %.1f", c.Evidence.Lift, minLift)
	}
	if c.WindowStart != start || c.WindowEnd != end {
		t.Error("window start/end mismatch")
	}
}

// TestInferIntroducedBy_OutsideWindow ensures deployments whose FirstSeen falls
// outside [start, end] produce no claims.
func TestInferIntroducedBy_OutsideWindow(t *testing.T) {
	start := baseTime
	end := baseTime.Add(60 * time.Minute)

	// Deploy is 2 hours before the window.
	deployTime := baseTime.Add(-2 * time.Hour)

	var failures []struct {
		reqID     string
		service   string
		errorCode string
		reqTime   time.Time
	}
	failTime := baseTime.Add(5 * time.Minute) // inside window
	for i := 0; i < 35; i++ {
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{
			reqID:     "req-out-" + string(rune('a'+i%26)),
			service:   "payment",
			errorCode: "PAY_404",
			reqTime:   failTime.Add(time.Duration(i) * time.Second),
		})
	}

	g := buildGraph(failures)
	deps := []DeploymentInfo{
		{ID: "deploy-old", Service: "payment", FirstSeen: deployTime},
	}

	claims := InferIntroducedBy(g, deps, start, end)

	if len(claims) != 0 {
		t.Errorf("expected 0 claims for out-of-window deploy, got %d", len(claims))
	}
}

// TestInferIntroducedBy_TwoDeploysAmbiguous verifies that two deployments for
// the same service in the window produce no claims (ambiguous attribution).
func TestInferIntroducedBy_TwoDeploysAmbiguous(t *testing.T) {
	start := baseTime
	end := baseTime.Add(60 * time.Minute)

	deploy1Time := baseTime.Add(5 * time.Minute)
	deploy2Time := baseTime.Add(20 * time.Minute)

	var failures []struct {
		reqID     string
		service   string
		errorCode string
		reqTime   time.Time
	}
	failTime := deploy1Time.Add(2 * time.Minute)
	for i := 0; i < 40; i++ {
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{
			reqID:     "req-amb-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			service:   "api-gateway",
			errorCode: "GW_500",
			reqTime:   failTime.Add(time.Duration(i) * time.Second),
		})
	}

	g := buildGraph(failures)
	deps := []DeploymentInfo{
		{ID: "deploy-a", Service: "api-gateway", FirstSeen: deploy1Time},
		{ID: "deploy-b", Service: "api-gateway", FirstSeen: deploy2Time},
	}

	claims := InferIntroducedBy(g, deps, start, end)

	if len(claims) != 0 {
		t.Errorf("expected 0 claims for ambiguous (2-deploy) service, got %d", len(claims))
	}
}

// TestInferIntroducedBy_BelowThreshold verifies that fewer than minAfterFailures
// failures after the deploy produce no claims.
func TestInferIntroducedBy_BelowThreshold(t *testing.T) {
	start := baseTime
	end := baseTime.Add(60 * time.Minute)
	deployTime := baseTime.Add(10 * time.Minute)

	// Only 20 failures — below the required 30.
	var failures []struct {
		reqID     string
		service   string
		errorCode string
		reqTime   time.Time
	}
	failTime := deployTime.Add(3 * time.Minute)
	for i := 0; i < 20; i++ {
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{
			reqID:     "req-low-" + string(rune('a'+i%26)),
			service:   "db-service",
			errorCode: "DB_TIMEOUT",
			reqTime:   failTime.Add(time.Duration(i) * time.Second),
		})
	}

	g := buildGraph(failures)
	deps := []DeploymentInfo{
		{ID: "deploy-db", Service: "db-service", FirstSeen: deployTime},
	}

	claims := InferIntroducedBy(g, deps, start, end)

	if len(claims) != 0 {
		t.Errorf("expected 0 claims (below minAfterFailures), got %d", len(claims))
	}
}

// TestInferIntroducedBy_Deterministic verifies that the same input always
// produces the same output in the same order.
func TestInferIntroducedBy_Deterministic(t *testing.T) {
	start := baseTime
	end := baseTime.Add(120 * time.Minute)

	// Two services each with one deployment and 35 post-deploy failures.
	svcA := struct{ name, code, deployID string }{"svc-alpha", "ALPHA_ERR", "deploy-alpha"}
	svcB := struct{ name, code, deployID string }{"svc-beta", "BETA_ERR", "deploy-beta"}

	deployTimeA := baseTime.Add(10 * time.Minute)
	deployTimeB := baseTime.Add(15 * time.Minute)

	var failures []struct {
		reqID     string
		service   string
		errorCode string
		reqTime   time.Time
	}
	for i := 0; i < 35; i++ {
		t_ := deployTimeA.Add(time.Duration(i+1) * time.Minute)
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{"req-a-" + string(rune('a'+i%26)), svcA.name, svcA.code, t_})
	}
	for i := 0; i < 35; i++ {
		t_ := deployTimeB.Add(time.Duration(i+1) * time.Minute)
		failures = append(failures, struct {
			reqID     string
			service   string
			errorCode string
			reqTime   time.Time
		}{"req-b-" + string(rune('a'+i%26)), svcB.name, svcB.code, t_})
	}

	g := buildGraph(failures)
	deps := []DeploymentInfo{
		{ID: svcA.deployID, Service: svcA.name, FirstSeen: deployTimeA},
		{ID: svcB.deployID, Service: svcB.name, FirstSeen: deployTimeB},
	}

	first := InferIntroducedBy(g, deps, start, end)
	second := InferIntroducedBy(g, deps, start, end)

	if len(first) != len(second) {
		t.Fatalf("non-deterministic: first=%d claims, second=%d claims", len(first), len(second))
	}
	for i := range first {
		if first[i].Service != second[i].Service || first[i].Subject != second[i].Subject {
			t.Errorf("claim[%d] differs between runs: first=%+v second=%+v", i, first[i], second[i])
		}
	}

	// Also verify ordering: svc-alpha < svc-beta lexicographically.
	if len(first) == 2 {
		if first[0].Service != svcA.name {
			t.Errorf("expected first claim for %q, got %q", svcA.name, first[0].Service)
		}
		if first[1].Service != svcB.name {
			t.Errorf("expected second claim for %q, got %q", svcB.name, first[1].Service)
		}
	}
}
