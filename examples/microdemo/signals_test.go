package microdemo

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDemoSignalPosterPostsDeployAndDependencySignals(t *testing.T) {
	var posted []map[string]any
	poster := NewDemoSignalPoster("http://ingest.example", "demo-write")
	poster.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/signals" {
			t.Fatalf("path = %s, want /v1/signals", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "demo-write" {
			t.Fatalf("api key = %q, want demo-write", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode signal: %v", err)
		}
		posted = append(posted, body)
		raw, _ := json.Marshal(map[string]any{
			"signal": map[string]any{"signal_id": "sig_" + body["type"].(string)},
		})
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(raw)),
		}, nil
	})}
	poster.now = func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	results := poster.PostDemoSignals(t.Context())
	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	for _, result := range results {
		if !result.Accepted || result.SignalID == "" || result.Status != http.StatusCreated {
			t.Fatalf("result = %+v", result)
		}
	}
	if len(posted) != 3 {
		t.Fatalf("posted len = %d, want 3", len(posted))
	}
	if posted[0]["type"] != "deploy" || posted[0]["service"] != "checkout" || posted[0]["env"] != "demo" {
		t.Fatalf("deploy signal = %+v", posted[0])
	}
	if posted[1]["type"] != "dependency" || posted[1]["service"] != "payment" || posted[1]["reason"] != "payment_gateway_5xx" {
		t.Fatalf("dependency signal = %+v", posted[1])
	}
	metadata, ok := posted[1]["metadata"].(map[string]any)
	if !ok || metadata["error_code"] != "PMT_502" {
		t.Fatalf("dependency metadata = %+v", posted[1]["metadata"])
	}
	// Infra runtime (OOMKill) targets checkout with env=demo so it correlates
	// onto the same incident as the alert/dependency evidence.
	if posted[2]["type"] != "runtime" || posted[2]["service"] != "checkout" || posted[2]["env"] != "demo" {
		t.Fatalf("runtime signal = %+v", posted[2])
	}
	if posted[2]["source"] != "k8s-demo" || posted[2]["reason"] != "OOMKilled" {
		t.Fatalf("runtime signal source/reason = %+v", posted[2])
	}
	rtMeta, ok := posted[2]["metadata"].(map[string]any)
	if !ok || rtMeta["subtype"] != "oom_killed" {
		t.Fatalf("runtime metadata = %+v", posted[2]["metadata"])
	}
}

func TestDemoSignalPosterReportsNonCreatedResponse(t *testing.T) {
	poster := NewDemoSignalPoster("http://ingest.example", "")
	poster.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(bytes.NewBufferString("set SQLITE_PATH to enable signals")),
		}, nil
	})}
	results := poster.PostDemoSignals(t.Context())
	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	for _, result := range results {
		if result.Accepted || result.Status != http.StatusServiceUnavailable || result.Error == "" {
			t.Fatalf("result = %+v", result)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
