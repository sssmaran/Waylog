package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
)

func makeTestServerWithColdStore(t *testing.T) (*Server, *coldstore.SQLiteStore) {
	t.Helper()
	managed, err := coldstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cs := managed.(*coldstore.SQLiteStore)
	t.Cleanup(func() { cs.Close() })
	srv := &Server{coldStore: cs}
	return srv, cs
}

func postDeploy(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.DeployRoute(w, req)
	return w
}

func TestDeployWebhook_HappyPath(t *testing.T) {
	srv, cs := makeTestServerWithColdStore(t)

	body := `{"id":"d1","service":"api-gateway","env":"prod","version":"v1.2.3"}`
	w := postDeploy(t, srv, body)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	dep, err := cs.DeploymentByID(context.Background(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if dep == nil {
		t.Fatal("deployment not found in store")
	}
	if dep.Service != "api-gateway" || dep.Env != "prod" || dep.Version != "v1.2.3" {
		t.Errorf("unexpected deployment fields: %+v", dep)
	}
}

func TestDeployWebhook_MissingFields(t *testing.T) {
	srv, _ := makeTestServerWithColdStore(t)

	cases := []struct {
		name string
		body string
	}{
		{"missing id", `{"service":"svc","env":"prod"}`},
		{"missing service", `{"id":"d1","env":"prod"}`},
		{"missing env", `{"id":"d1","service":"svc"}`},
		{"empty body fields", `{"id":"","service":"","env":""}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postDeploy(t, srv, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeployWebhook_EnvConflict(t *testing.T) {
	srv, _ := makeTestServerWithColdStore(t)

	// First insert: env=prod
	w := postDeploy(t, srv, `{"id":"d1","service":"svc","env":"prod"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("first insert expected 201, got %d", w.Code)
	}

	// Second insert: same ID, different env
	w = postDeploy(t, srv, `{"id":"d1","service":"svc","env":"staging"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 on env conflict, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeployWebhook_DuplicateOK(t *testing.T) {
	srv, _ := makeTestServerWithColdStore(t)

	body := `{"id":"d1","service":"svc","env":"prod"}`

	w := postDeploy(t, srv, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("first insert expected 201, got %d", w.Code)
	}

	w = postDeploy(t, srv, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("second insert (same env) expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeployRoute_MethodNotAllowed(t *testing.T) {
	srv, _ := makeTestServerWithColdStore(t)

	req := httptest.NewRequest(http.MethodPut, "/v1/deployments", nil)
	w := httptest.NewRecorder()
	srv.DeployRoute(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestDeployments_GET(t *testing.T) {
	srv, _ := makeTestServerWithColdStore(t)

	// Insert a deployment first
	w := postDeploy(t, srv, `{"id":"d1","service":"api-gateway","env":"prod","version":"v1.0.0"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("insert expected 201, got %d", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments?window=1h", nil)
	wr := httptest.NewRecorder()
	srv.DeployRoute(wr, req)

	if wr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wr.Code, wr.Body.String())
	}

	var wrapper struct {
		Deployments []deployResponse `json:"deployments"`
	}
	if err := json.Unmarshal(wr.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(wrapper.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(wrapper.Deployments))
	}
	if wrapper.Deployments[0].ID != "d1" || wrapper.Deployments[0].Service != "api-gateway" {
		t.Errorf("unexpected deployment: %+v", wrapper.Deployments[0])
	}
}

const tsFormat = "2006-01-02T15:04:05.000000000Z07:00"

func insertTestEvent(t *testing.T, cs *coldstore.SQLiteStore, service, env string, success bool, ts time.Time) {
	t.Helper()
	successInt := 1
	if !success {
		successInt = 0
	}
	_, err := cs.WriterForTest().ExecContext(context.Background(),
		`INSERT INTO events (trace_id, event_name, service, env, user_id, status_code, success, latency_ms, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"aaaa0000bbbb1111cccc2222dddd3333",
		service+".request",
		service,
		env,
		"user1",
		200,
		successInt,
		10,
		ts.UTC().Format(tsFormat),
	)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func TestDeployments_ErrorRateNormal(t *testing.T) {
	srv, cs := makeTestServerWithColdStore(t)

	// Insert a deployment with a known first_seen time
	depTime := time.Now().UTC().Add(-30 * time.Minute)
	dep := coldstore.Deployment{
		ID:        "d1",
		Service:   "payment",
		Env:       "prod",
		FirstSeen: depTime,
		LastSeen:  depTime,
	}
	if err := cs.UpsertDeployment(context.Background(), dep); err != nil {
		t.Fatal(err)
	}

	// 100 events before deployment (2 failures = 2% error rate)
	for i := 0; i < 98; i++ {
		insertTestEvent(t, cs, "payment", "prod", true, depTime.Add(-2*time.Minute))
	}
	for i := 0; i < 2; i++ {
		insertTestEvent(t, cs, "payment", "prod", false, depTime.Add(-2*time.Minute))
	}

	// 100 events after deployment (10 failures = 10% error rate)
	for i := 0; i < 90; i++ {
		insertTestEvent(t, cs, "payment", "prod", true, depTime.Add(2*time.Minute))
	}
	for i := 0; i < 10; i++ {
		insertTestEvent(t, cs, "payment", "prod", false, depTime.Add(2*time.Minute))
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments?window=1h", nil)
	wr := httptest.NewRecorder()
	srv.DeployRoute(wr, req)

	if wr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wr.Code, wr.Body.String())
	}

	var wrapper struct {
		Deployments []deployResponse `json:"deployments"`
	}
	if err := json.Unmarshal(wr.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(wrapper.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(wrapper.Deployments))
	}

	r := wrapper.Deployments[0]
	if r.ErrorRateChange == nil {
		t.Fatal("expected error_rate_change to be non-nil")
	}
	// before: 2/100 = 0.02, after: 10/100 = 0.10, change ~ 5.0
	if *r.ErrorRateChange < 4.0 || *r.ErrorRateChange > 6.0 {
		t.Errorf("expected error_rate_change ~5.0, got %f", *r.ErrorRateChange)
	}
	if r.BeforeRequests != 100 || r.AfterRequests != 100 {
		t.Errorf("expected 100 before and after requests, got before=%d after=%d", r.BeforeRequests, r.AfterRequests)
	}
}

func TestDeployments_ErrorRateInsufficient(t *testing.T) {
	srv, cs := makeTestServerWithColdStore(t)

	depTime := time.Now().UTC().Add(-30 * time.Minute)
	dep := coldstore.Deployment{
		ID:        "d1",
		Service:   "payment",
		Env:       "prod",
		FirstSeen: depTime,
		LastSeen:  depTime,
	}
	if err := cs.UpsertDeployment(context.Background(), dep); err != nil {
		t.Fatal(err)
	}

	// Only 5 events before and 5 after (below minRequests=10)
	for i := 0; i < 5; i++ {
		insertTestEvent(t, cs, "payment", "prod", true, depTime.Add(-2*time.Minute))
	}
	for i := 0; i < 5; i++ {
		insertTestEvent(t, cs, "payment", "prod", false, depTime.Add(2*time.Minute))
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments?window=1h", nil)
	wr := httptest.NewRecorder()
	srv.DeployRoute(wr, req)

	if wr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wr.Code, wr.Body.String())
	}

	var wrapper struct {
		Deployments []deployResponse `json:"deployments"`
	}
	if err := json.Unmarshal(wr.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(wrapper.Deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(wrapper.Deployments))
	}

	r := wrapper.Deployments[0]
	if r.ErrorRateChange != nil {
		t.Errorf("expected error_rate_change to be nil when < 10 requests, got %f", *r.ErrorRateChange)
	}
	if r.BeforeRequests != 5 || r.AfterRequests != 5 {
		t.Errorf("expected 5 before and after requests, got before=%d after=%d", r.BeforeRequests, r.AfterRequests)
	}
}
