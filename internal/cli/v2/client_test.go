package cliv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := map[string]string{
		"":               defaultBaseURL,
		":8080":          "http://localhost:8080",
		"localhost:8080": "http://localhost:8080",
		"http://x/":      "http://x",
	}
	for in, want := range tests {
		if got := NormalizeBaseURL(in); got != want {
			t.Fatalf("NormalizeBaseURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClientSerializesAuthAndQuery(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(EventSearchResponse{})
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{BaseURL: srv.URL, APIKey: "read", Timeout: time.Second})
	if _, err := client.Search(context.Background(), SearchParams{ErrorCode: "PMT_502", Service: "checkout", Status: "error", Window: "15m", Limit: 10, Cursor: "abc"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/events/search" || gotAuth != "Bearer read" {
		t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
	}
	for _, want := range []string{"error_code=PMT_502", "service=checkout", "status=error", "window=15m", "limit=10", "cursor=abc"} {
		if !containsQuery(gotQuery, want) {
			t.Fatalf("query=%q missing %q", gotQuery, want)
		}
	}
}

func TestClientDecodesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"missing","detail":"gone"}}`))
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{BaseURL: srv.URL, Timeout: time.Second})
	_, err := client.Trace(context.Background(), "missing")
	api, ok := err.(*APIError)
	if !ok || api.Code != "not_found" || api.Detail != "gone" || exitCodeForError(err) != 3 {
		t.Fatalf("err=%#v", err)
	}
}

func containsQuery(raw, want string) bool {
	for _, part := range strings.Split(raw, "&") {
		if part == want {
			return true
		}
	}
	return false
}

func TestClientTriageBuildsExpectedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/triage/inc_abc" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("snapshot") != "true" {
			t.Fatalf("snapshot query missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version":"triage.v1",
			"incident_ref":{"id":"inc_abc","window":"15m"},
			"confidence":"medium",
			"generated_at":"t",
			"report_hash":"sha256:x"
		}`))
	}))
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL, APIKey: "test-key", Timeout: 5 * time.Second})
	rep, err := c.Triage(context.Background(), "inc_abc", TriageParams{Snapshot: true})
	if err != nil {
		t.Fatalf("triage: %v", err)
	}
	if rep.IncidentRef.ID != "inc_abc" {
		t.Fatalf("got id %q", rep.IncidentRef.ID)
	}
}
