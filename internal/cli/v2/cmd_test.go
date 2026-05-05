package cliv2

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunCLIRequiresV2Reads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CapabilitiesResponse{})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "errors"}, nil, &stdout, &stderr)
	if code != 3 || !strings.Contains(stderr.String(), "WAYLOG_V2_READS=true") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLIErrorsHappyPath(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"window":"15m0s","rows":[],"next_cursor":null}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "errors", "--window", "15m", "--service", "checkout"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if gotPath != "/v1/errors" || !strings.Contains(gotQuery, "window=15m") || !strings.Contains(gotQuery, "service=checkout") {
		t.Fatalf("path=%q query=%q", gotPath, gotQuery)
	}
	if !strings.Contains(stdout.String(), "No error families found.") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunCLIRecentSerializesFilters(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"traces":[],"next_cursor":null}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "recent", "--window", "15m", "--service", "checkout", "--status", "error,timeout", "--limit", "5", "--cursor", "abc", "--include-suppressed"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"window=15m", "service=checkout", "status=error%2Ctimeout", "limit=5", "cursor=abc", "include_suppressed=true"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q missing %q", gotQuery, want)
		}
	}
	if gotPath != "/v1/traces/recent" {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestRunCLIIncidentsListsActive(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"incidents":[{"incident_id":"inc_1234567890abcdef","env":"prod","service":"checkout","error_family":{"service":"checkout","step":"payment.charge","error_code":"PMT_502"},"status":"active","cause":"dependency","confidence":"medium","severity":8,"started_at":"2026-05-04T12:00:00Z","updated_at":"2026-05-04T12:01:00Z","last_seen_at":"2026-05-04T12:01:00Z","affected_requests":12,"affected_services":3,"top_services":["checkout","payment"],"sample_traces":["trace-a"],"evidence":[],"next_checks":["check payment"],"lift":6,"baseline_count":2,"current_count":12}]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "incidents"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if gotPath != "/v1/incidents/active" {
		t.Fatalf("path=%q", gotPath)
	}
	for _, want := range []string{"INCIDENT", "dependency", "checkout:payment.charge:PMT_502"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunCLIIncidentsEmptyAndRequiresV2Reads(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"v2_reads":{"enabled":false}}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "incidents"}, nil, &stdout, &stderr)
	if code != 3 || calls != 1 || !strings.Contains(stderr.String(), "WAYLOG_V2_READS=true") {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls, stdout.String(), stderr.String())
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"incidents":[]}`))
	})
	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"--addr", srv.URL, "incidents"}, nil, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "No active incidents.") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLIIncidentDetailAndSnapshot(t *testing.T) {
	calls := []string{}
	accepts := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		calls = append(calls, r.URL.String())
		accepts = append(accepts, r.Header.Get("Accept"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/snapshot") && r.Header.Get("Accept") == "application/json":
			_, _ = w.Write([]byte(`{"snapshot":"Incident inc/1\n","incident":{"incident_id":"inc/1","env":"prod","service":"checkout","error_family":{"service":"checkout","step":"payment.charge","error_code":"PMT_502"},"status":"active","cause":"dependency","confidence":"medium","severity":8,"started_at":"2026-05-04T12:00:00Z","updated_at":"2026-05-04T12:01:00Z","last_seen_at":"2026-05-04T12:01:00Z","affected_requests":12,"affected_services":3,"top_services":["checkout","payment"],"sample_traces":["trace-a"],"evidence":[],"next_checks":["check payment"],"lift":6,"baseline_count":2,"current_count":12}}`))
		case strings.HasSuffix(r.URL.Path, "/snapshot"):
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("Incident inc/1\n"))
		default:
			_, _ = w.Write([]byte(`{"incident":{"incident_id":"inc/1","env":"prod","service":"checkout","error_family":{"service":"checkout","step":"payment.charge","error_code":"PMT_502"},"status":"active","cause":"dependency","confidence":"medium","severity":8,"started_at":"2026-05-04T12:00:00Z","updated_at":"2026-05-04T12:01:00Z","last_seen_at":"2026-05-04T12:01:00Z","affected_requests":12,"affected_services":3,"top_services":["checkout","payment"],"sample_traces":["trace-a"],"evidence":[{"kind":"trace","title":"sample","trace_id":"trace-a","occurred_at":"2026-05-04T12:00:00Z"}],"next_checks":["check payment"],"lift":6,"baseline_count":2,"current_count":12}}`))
		}
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "incident", "inc/1"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("detail code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if calls[0] != "/v1/incidents/inc%2F1" || !strings.Contains(stdout.String(), "incident_id: inc/1") {
		t.Fatalf("calls=%v stdout=%q", calls, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"--addr", srv.URL, "incident", "inc/1", "--snapshot"}, nil, &stdout, &stderr)
	if code != 0 || stdout.String() != "Incident inc/1\n" {
		t.Fatalf("snapshot code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunCLI([]string{"--addr", srv.URL, "--json", "incident", "inc/1", "--snapshot"}, nil, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"snapshot"`) || accepts[len(accepts)-1] != "application/json" {
		t.Fatalf("json snapshot code=%d accepts=%v stdout=%q stderr=%q", code, accepts, stdout.String(), stderr.String())
	}
}

func TestRunCLIEventEscapesIDAndRequiresV2Reads(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.String())
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"event":{"event_id":"event/1","trace_id":"trace","service":"checkout","status":"ok","duration_ms":3}}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "event", "event/1"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(calls) != 2 || calls[1] != "/v1/events/event%2F1" {
		t.Fatalf("calls=%v", calls)
	}
	if !strings.Contains(stdout.String(), "event_id: event/1") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunCLICapabilitiesDoesNotRequireV2Reads(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"v2_reads":{"enabled":false},"otlp":{"http_traces":true}}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "capabilities"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if gotPath != "/v1/capabilities" {
		t.Fatalf("path=%q", gotPath)
	}
	if !strings.Contains(stdout.String(), "v2_reads: disabled") || !strings.Contains(stdout.String(), "otlp_http_traces: enabled") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunCLIExplainFallsBackToTraceID(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		calls = append(calls, r.URL.RawQuery)
		if r.URL.Query().Get("event_id") != "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"missing"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"trace_id":"trace","service":"checkout","status":"ok","anchor":null,"path":[],"logs":[],"downstream":[],"linkage":"timestamp_fallback"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "explain", "trace"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "event_id=trace") || !strings.Contains(calls[1], "trace_id=trace") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRunCLIBlastDisplayFamilyAllowsTrailingWindow(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"key":{"service":"checkout","step":"payment.charge","error_code":"PMT_502"},"view_mode":"single_family","window":"15m","affected_requests":1,"affected_services":2,"top_services":["checkout","payment"],"sample_traces":["trace"]}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "blast", "checkout:payment.charge:PMT_502", "--window", "15m"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if gotPath != "/v1/blast_radius" || !strings.Contains(gotQuery, "error_family=checkout%3Apayment.charge%3APMT_502") || !strings.Contains(gotQuery, "window=15m") {
		t.Fatalf("path=%q query=%q", gotPath, gotQuery)
	}
}

func TestRunCLISearchAllowsTrailingFilters(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities" {
			_, _ = w.Write([]byte(`{"v2_reads":{"enabled":true}}`))
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"events":[],"next_cursor":null}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--addr", srv.URL, "search", "PMT_502", "--window", "15m", "--limit", "5"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"error_code=PMT_502", "window=15m", "limit=5"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query=%q missing %q", gotQuery, want)
		}
	}
	if gotPath != "/v1/events/search" {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestRunCLIUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"search"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
