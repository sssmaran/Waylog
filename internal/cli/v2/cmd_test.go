package cliv2

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestParseTriageArgs(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantID   string
		wantSnap bool
		wantWin  string
		wantErr  bool
	}{
		{"id only", []string{"inc_abc"}, "inc_abc", false, "", false},
		{"id + snapshot", []string{"inc_abc", "--snapshot"}, "inc_abc", true, "", false},
		{"id + window", []string{"inc_abc", "--window", "30m"}, "inc_abc", false, "30m", false},
		{"id + window=30m", []string{"inc_abc", "--window=30m"}, "inc_abc", false, "30m", false},
		{"missing id", []string{}, "", false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, win, snap, err := parseTriageArgs(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if id != tc.wantID || snap != tc.wantSnap || win != tc.wantWin {
				t.Fatalf("got id=%q win=%q snap=%v want %q %q %v", id, win, snap, tc.wantID, tc.wantWin, tc.wantSnap)
			}
		})
	}
}
