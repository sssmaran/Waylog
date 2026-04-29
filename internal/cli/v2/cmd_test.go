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

func TestRunCLIUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"search"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
