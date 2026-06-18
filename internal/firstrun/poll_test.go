package firstrun

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitForIncidentThenReport(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/incidents/active":
			calls++
			if calls < 2 {
				w.Write([]byte(`{"incidents":[]}`))
				return
			}
			w.Write([]byte(`{"incidents":[{"incident_id":"inc_demo_1"}]}`))
		case r.URL.Path == "/v1/triage/inc_demo_1":
			// report_hash is at the top level of the triage report JSON
			// (pkg/triage.Report has ReportHash string `json:"report_hash"` directly)
			w.Write([]byte(`{"report_hash":"abc123","schema_version":"triage.v1","incident_ref":{"id":"inc_demo_1","window":"10m"},"confidence":"high","generated_at":"2026-01-01T00:00:00Z"}`))
		case r.URL.Path == "/v1/triage/inc_demo_1/report":
			w.Write([]byte("# Incident inc_demo_1\nreport_hash: abc123\n"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	out, err := waitForReport(reportPoll{
		IngestURL: srv.URL,
		ReadKey:   "",
		Timeout:   3 * time.Second,
		Interval:  10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("waitForReport: %v", err)
	}
	if out.IncidentID != "inc_demo_1" {
		t.Fatalf("IncidentID = %q, want inc_demo_1", out.IncidentID)
	}
	if out.ReportHash != "abc123" {
		t.Fatalf("ReportHash = %q, want abc123", out.ReportHash)
	}
	if out.Markdown == "" {
		t.Fatal("Markdown report must be populated")
	}
}

func TestWaitForIncidentTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"incidents":[]}`))
	}))
	defer srv.Close()
	_, err := waitForReport(reportPoll{IngestURL: srv.URL, Timeout: 100 * time.Millisecond, Interval: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error when no incident opens")
	}
}
