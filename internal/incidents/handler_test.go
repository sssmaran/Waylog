package incidents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	var active ActiveResponse
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
