package integration

import (
	"net/http"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func TestIncident_DeployFailure(t *testing.T) {
	srv, cs, bw := newIntegrationServer(t)

	// 1. Send 100 healthy baseline events for payment-service.
	ingestEvents(t, srv, makeHealthyEvents(100, "payment-service"))

	// 2. Register deployment via webhook.
	w := httpPOST(t, srv.DeployRoute, "/v1/deployments", map[string]string{
		"id":      "deploy_v2.1",
		"service": "payment-service",
		"env":     "prod",
		"version": "v2.1.0",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy webhook: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Inject 50 failure events with error_code=PMT_502.
	ingestEvents(t, srv, makeFailureEvents(50, "payment-service", "PMT_502"))

	// 4. Verify overview shows error rate increased.
	ow := httpGET(t, srv.Overview, "/v1/overview?window=10m")
	if ow.Code != http.StatusOK {
		t.Fatalf("overview: expected 200, got %d", ow.Code)
	}
	var overview map[string]any
	decodeJSON(t, ow, &overview)

	errorRate, _ := overview["error_rate"].(float64)
	if errorRate < 20 {
		t.Errorf("expected error_rate >= 20%% (50/150), got %f", errorRate)
	}

	// 5. Verify recent traces include failures.
	rw := httpGET(t, srv.RecentTraces, "/v1/traces/recent?limit=10&failures_only=true")
	if rw.Code != http.StatusOK {
		t.Fatalf("recent traces: expected 200, got %d", rw.Code)
	}
	var recentResp struct {
		Traces []struct {
			TraceID string `json:"trace_id"`
			Success bool   `json:"success"`
			Service string `json:"service"`
		} `json:"traces"`
		TotalCount int `json:"total_count"`
	}
	decodeJSON(t, rw, &recentResp)
	if len(recentResp.Traces) == 0 {
		t.Fatal("expected at least one failed trace")
	}
	if recentResp.Traces[0].Success {
		t.Error("expected first trace to be a failure")
	}

	// 6. Verify deployment is listed via GET /v1/deployments.
	dw := httpGET(t, srv.DeployRoute, "/v1/deployments?window=1h")
	if dw.Code != http.StatusOK {
		t.Fatalf("deployments GET: expected 200, got %d", dw.Code)
	}
	var deployResp struct {
		Deployments []struct {
			ID      string `json:"id"`
			Service string `json:"service"`
		} `json:"deployments"`
	}
	decodeJSON(t, dw, &deployResp)
	found := false
	for _, d := range deployResp.Deployments {
		if d.ID == "deploy_v2.1" && d.Service == "payment-service" {
			found = true
			break
		}
	}
	if !found {
		t.Error("deploy_v2.1 not found in deployments list")
	}

	// 7. Verify cold store has events (flush batch writer first).
	flushColdWriter(t, bw)
	page, err := cs.SearchEvents(coldstore.SearchFilter{
		Service:   "payment-service",
		ErrorCode: "PMT_502",
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount < 50 {
		t.Errorf("expected >= 50 PMT_502 events in cold store, got %d", page.TotalCount)
	}
}

func TestIncident_TraceStory(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	// Ingest a multi-span trace: api-gateway → checkout → payment (fails).
	traceID := "aaaa1111bbbb2222cccc3333dddd4444"
	events := []event.WideEvent{
		testutil.MakeEvent(
			testutil.WithService("api-gateway"),
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("1111111111111111"),
			testutil.WithError("GW_502", "upstream failure"),
			testutil.WithStatusCode(502),
			testutil.WithCallerService(""),
		),
		testutil.MakeEvent(
			testutil.WithService("checkout"),
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("2222222222222222"),
			testutil.WithParentSpanID("1111111111111111"),
			testutil.WithError("CHK_502", "downstream failure"),
			testutil.WithStatusCode(502),
			testutil.WithCallerService("api-gateway"),
		),
		testutil.MakeEvent(
			testutil.WithService("payment"),
			testutil.WithTraceID(traceID),
			testutil.WithSpanID("3333333333333333"),
			testutil.WithParentSpanID("2222222222222222"),
			testutil.WithError("PMT_502", "payment timeout"),
			testutil.WithStatusCode(502),
			testutil.WithCallerService("checkout"),
		),
	}
	ingestEvents(t, srv, events)

	// Verify trace story.
	sw := httpGET(t, srv.TraceStory, "/v1/traces/story?trace_id="+traceID)
	if sw.Code != http.StatusOK {
		t.Fatalf("trace story: expected 200, got %d: %s", sw.Code, sw.Body.String())
	}

	var storyResp map[string]any
	decodeJSON(t, sw, &storyResp)
	story, ok := storyResp["story"].(map[string]any)
	if !ok {
		t.Fatal("trace story missing 'story' field")
	}
	if _, ok := story["chain"]; !ok {
		t.Error("trace story missing 'chain' field")
	}
}
