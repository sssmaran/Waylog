package integration

import (
	"net/http"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
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

	// 4. Verify deployment is listed via GET /v1/deployments.
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

	// 6. Verify cold store has events (flush batch writer first).
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
