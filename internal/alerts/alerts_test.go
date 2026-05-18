package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func TestNormalizeWaylogAlert(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"source":"waylog","alert_id":"alert_1","service":"checkout","env":"prod","severity":"critical","reason":"PMT_502 spike","error_code":"PMT_502","provider_url":"https://alerts/1","timestamp":"2026-05-10T12:00:00Z"}`)
	sig, err := Normalize(raw, now)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if sig.Type != signals.TypeAlert || sig.Source != "waylog" || sig.Service != "checkout" {
		t.Fatalf("unexpected signal: %+v", sig)
	}
	if sig.Metadata["alert_id"] != "alert_1" || sig.Metadata["error_code"] != "PMT_502" {
		t.Fatalf("metadata not preserved: %+v", sig.Metadata)
	}
}

func TestNormalizeProviderPayloads(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		raw    string
		source string
	}{
		{
			name:   "alertmanager",
			raw:    `{"receiver":"team","alerts":[{"fingerprint":"fp1","startsAt":"2026-05-10T12:00:00Z","generatorURL":"https://am/1","labels":{"alertname":"Payment502","service":"checkout","env":"prod","severity":"critical","error_code":"PMT_502"},"annotations":{"summary":"PMT_502 spike"}}]}`,
			source: "alertmanager",
		},
		{
			name:   "grafana",
			raw:    `{"ruleUID":"rule1","title":"Payment failures","state":"alerting","alerts":[{"startsAt":"2026-05-10T12:00:00Z","dashboardURL":"https://grafana/d","labels":{"service":"checkout","env":"prod","severity":"critical","error_code":"PMT_502"},"annotations":{"summary":"PMT_502 spike"}}]}`,
			source: "grafana",
		},
		{
			name:   "pagerduty",
			raw:    `{"messages":[{"event":{"event_type":"incident.trigger","data":{"id":"pd1","html_url":"https://pd/1","title":"PMT_502 spike","service":{"summary":"checkout"},"env":"prod","urgency":"high","error_code":"PMT_502"}}}]}`,
			source: "pagerduty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig, err := Normalize([]byte(tc.raw), now)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if sig.Source != tc.source || sig.Type != signals.TypeAlert {
				t.Fatalf("unexpected signal: %+v", sig)
			}
			if sig.Service != "checkout" || sig.Env != "prod" {
				t.Fatalf("service/env missing: %+v", sig)
			}
		})
	}
}

func TestMatcherOrderAndUnmatched(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	inc := incidents.Incident{
		IncidentID: "inc_1",
		Env:        "prod",
		Status:     incidents.StatusActive,
		StartedAt:  now.Add(-time.Minute),
		UpdatedAt:  now,
		ErrorFamily: apiv2.ErrorFamily{
			Service:   "checkout",
			Step:      "payment.charge",
			ErrorCode: "PMT_502",
		},
	}
	h := NewHandler(&memSignalStore{}, incidentSource{rows: []incidents.Incident{inc}}, traceResolver{}, 15*time.Minute)

	sig := &signals.Signal{Env: "prod", Service: "checkout", Timestamp: now, Metadata: map[string]any{"incident_id": "inc_1", "error_code": "OTHER"}}
	got := h.Match(context.Background(), sig)
	if !got.Matched || got.Strategy != "incident_id" {
		t.Fatalf("incident id should win, got %+v", got)
	}

	sig = &signals.Signal{Env: "prod", Service: "checkout", Timestamp: now, Metadata: map[string]any{"error_code": "PMT_502"}}
	got = h.Match(context.Background(), sig)
	if !got.Matched || got.Strategy != "family" {
		t.Fatalf("family match failed: %+v", got)
	}

	sig = &signals.Signal{Env: "prod", Service: "checkout", Timestamp: now.Add(2 * time.Hour), Metadata: map[string]any{"error_code": "PMT_502"}}
	got = h.Match(context.Background(), sig)
	if got.Matched || got.Strategy != "none" {
		t.Fatalf("outside window should be unmatched: %+v", got)
	}
}

func TestMatcherExplicitIncidentIDCanMatchResolvedIncident(t *testing.T) {
	inc := incidents.Incident{IncidentID: "inc_resolved", Status: incidents.StatusResolved}
	h := NewHandler(&memSignalStore{}, incidentSource{rows: []incidents.Incident{inc}}, traceResolver{}, 15*time.Minute)

	got := h.Match(context.Background(), &signals.Signal{Metadata: map[string]any{"incident_id": "inc_resolved"}})
	if !got.Matched || got.Strategy != "incident_id" || got.IncidentID != "inc_resolved" {
		t.Fatalf("explicit incident_id should be authoritative, got %+v", got)
	}
}

func TestNormalizeGrafanaAlertmanagerPayload(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"receiver":"grafana","status":"firing","externalURL":"https://grafana.example","alerts":[{"startsAt":"2026-05-10T12:00:00Z","dashboardURL":"https://grafana.example/d/abc","labels":{"service":"checkout","env":"prod","severity":"critical","error_code":"PMT_502"},"annotations":{"summary":"PMT_502 spike","__dashboardUid__":"abc"}}]}`)
	sig, err := Normalize(raw, now)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if sig.Source != "grafana" {
		t.Fatalf("source=%q want grafana", sig.Source)
	}
	if sig.Metadata["provider_url"] != "https://grafana.example/d/abc" {
		t.Fatalf("provider_url not preserved: %+v", sig.Metadata)
	}
}

func TestNormalizeRejectsUnsupported(t *testing.T) {
	_, err := Normalize([]byte(`{"hello":"world"}`), time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	var normErr *NormalizeError
	if !errors.As(err, &normErr) || normErr.Code != CodeUnsupportedAlert {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandlerStoresWaylogAlert(t *testing.T) {
	store := &recordingSignalStore{}
	h := NewHandler(store, incidentSource{}, traceResolver{}, 15*time.Minute)
	h.now = func() time.Time { return time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(`{"source":"waylog","alert_id":"alert_1","service":"checkout","env":"prod","severity":"critical","reason":"PMT_502 spike"}`))
	rr := httptest.NewRecorder()
	h.Alerts(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if store.inserted == nil || store.inserted.Type != signals.TypeAlert {
		t.Fatalf("alert signal was not inserted: %+v", store.inserted)
	}
	var out struct {
		Match MatchResult `json:"match"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Match.Matched || out.Match.Strategy != "none" {
		t.Fatalf("unexpected match for no active incidents: %+v", out.Match)
	}
}

func TestHandlerRejectsInvalidJSON(t *testing.T) {
	h := NewHandler(&recordingSignalStore{}, incidentSource{}, traceResolver{}, 15*time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(`{`))
	rr := httptest.NewRecorder()
	h.Alerts(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerUnavailableSignalStore(t *testing.T) {
	h := NewHandler(signals.UnavailableStore{}, incidentSource{}, traceResolver{}, 15*time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/v1/alerts", strings.NewReader(`{"source":"waylog","alert_id":"alert_1","service":"checkout","env":"prod","reason":"PMT_502 spike"}`))
	rr := httptest.NewRecorder()
	h.Alerts(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

type incidentSource struct {
	rows []incidents.Incident
}

func (s incidentSource) Active(context.Context) ([]incidents.Incident, error) {
	return s.rows, nil
}

func (s incidentSource) Get(_ context.Context, id string) (incidents.Incident, error) {
	for _, inc := range s.rows {
		if inc.IncidentID == id {
			return inc, nil
		}
	}
	return incidents.Incident{}, incidents.ErrNotFound
}

type traceResolver struct{}

func (traceResolver) TraceStoryByTraceID(string) (apiv2.StoryResponse, bool) {
	return apiv2.StoryResponse{Service: "checkout", Anchor: &apiv2.StoryAnchor{Step: "payment.charge", ErrorCode: "PMT_502"}}, true
}

type memSignalStore struct{}

func (*memSignalStore) Insert(context.Context, *signals.Signal) error { return nil }
func (*memSignalStore) Query(context.Context, signals.Filter) ([]signals.Signal, error) {
	return nil, nil
}
func (*memSignalStore) PruneOlderThan(context.Context, time.Time) (int, error) { return 0, nil }

type recordingSignalStore struct {
	inserted *signals.Signal
}

func (s *recordingSignalStore) Insert(_ context.Context, sig *signals.Signal) error {
	copy := *sig
	s.inserted = &copy
	return nil
}
func (*recordingSignalStore) Query(context.Context, signals.Filter) ([]signals.Signal, error) {
	return nil, nil
}
func (*recordingSignalStore) PruneOlderThan(context.Context, time.Time) (int, error) {
	return 0, nil
}
