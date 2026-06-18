package incidents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func TestHandlerActiveDetailAndSnapshot(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	inc := testIncident(now)
	if err := store.Upsert(context.Background(), inc); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(&fakeReader{}, nil, nil, store, Config{}, nil, nil)
	if err := engine.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(engine)

	rec := httptest.NewRecorder()
	h.Active(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents/active", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("active status=%d body=%s", rec.Code, rec.Body.String())
	}
	var active apiv2.IncidentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if len(active.Incidents) != 1 || active.Incidents[0].IncidentID != inc.IncidentID {
		t.Fatalf("active=%+v", active)
	}

	rec = httptest.NewRecorder()
	h.Incident(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents/"+inc.IncidentID, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), inc.IncidentID) {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Incident(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents/"+inc.IncidentID+"/snapshot", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("snapshot status=%d content-type=%s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "Incident "+inc.IncidentID) {
		t.Fatalf("snapshot=%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/incidents/"+inc.IncidentID+"/snapshot", nil)
	req.Header.Set("Accept", "application/json")
	h.Incident(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"snapshot"`) {
		t.Fatalf("json snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_IncidentDetail_EmitsPropagationAndBlast(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	users := 12
	inc := Incident{
		IncidentID:  "inc_emit",
		Env:         "demo",
		Service:     "payment-service",
		ErrorFamily: apiv2.ErrorFamily{Service: "payment-service", Step: "charge", ErrorCode: "DB_TIMEOUT"},
		Status:      StatusActive,
		Cause:       CauseUnknown,
		Confidence:  ConfidenceMedium,
		Severity:    2,
		StartedAt:   ts,
		UpdatedAt:   ts,
		LastSeenAt:  ts,
		Propagation: &PropagationSnapshot{
			Latest: &PropagationEvidence{OriginService: "payment-service", OriginStep: "charge", SampleTraceID: "tx", CapturedAt: ts, CaptureStatus: CaptureOK},
		},
		Blast: &BlastSnapshot{
			Latest: &BlastEvidence{AffectedRequests: 5, AffectedServices: 2, AffectedUsers: &users, TopServices: []string{"checkout"}, CapturedAt: ts, CaptureStatus: CaptureOK},
		},
		Alerts: &AlertSnapshot{
			Latest: &AlertEvidence{
				Matches:       []MatchedAlert{{SignalID: "sig_1", AlertID: "CheckoutPaymentFailure", Source: "alertmanager", Severity: "critical", Reason: "PMT_502 spike", MatchedAt: ts, Strategy: "family"}},
				CapturedAt:    ts,
				CaptureStatus: CaptureOK,
			},
		},
	}
	store := NewMemoryStore()
	if err := store.Upsert(context.Background(), inc); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(&fakeReader{}, nil, nil, store, Config{}, nil, nil)
	if err := engine.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(engine)

	rec := httptest.NewRecorder()
	h.Incident(rec, httptest.NewRequest(http.MethodGet, "/v1/incidents/inc_emit", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got apiv2.IncidentDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Incident.Propagation == nil || got.Incident.Propagation.Latest == nil {
		t.Errorf("response missing .incident.propagation.latest: %s", rec.Body.String())
	}
	if got.Incident.Blast == nil || got.Incident.Blast.Latest == nil {
		t.Errorf("response missing .incident.blast.latest: %s", rec.Body.String())
	}
	if got.Incident.Blast.Latest.AffectedRequests != 5 {
		t.Errorf("Blast.Latest.AffectedRequests = %d; want 5", got.Incident.Blast.Latest.AffectedRequests)
	}
	if got.Incident.Alerts == nil || got.Incident.Alerts.Latest == nil {
		t.Errorf("response missing .incident.alerts.latest: %s", rec.Body.String())
	}
	if len(got.Incident.Alerts.Latest.Matches) != 1 || got.Incident.Alerts.Latest.Matches[0].SignalID != "sig_1" {
		t.Errorf("Alerts.Latest.Matches = %+v", got.Incident.Alerts.Latest.Matches)
	}
}
