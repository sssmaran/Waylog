package waylogv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestPostSignalPostsToSignalsEndpoint(t *testing.T) {
	t.Cleanup(resetForTest)

	var (
		mu      sync.Mutex
		gotPath string
		gotAuth string
		gotBody Signal
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	if err := Init(Config{Service: "checkout", Env: "demo", IngestURL: srv.URL, APIKey: "k1", EnableRuntimeHooks: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err := PostSignal(context.Background(), Signal{
		Type:     "runtime",
		Severity: "critical",
		Reason:   "panic: boom",
		Source:   "go-sdk",
		Metadata: map[string]any{"subtype": "panic"},
	})
	if err != nil {
		t.Fatalf("PostSignal: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/signals" {
		t.Errorf("path = %q, want /v1/signals", gotPath)
	}
	if gotAuth != "Bearer k1" {
		t.Errorf("auth = %q, want Bearer k1", gotAuth)
	}
	if gotBody.Service != "checkout" || gotBody.Env != "demo" {
		t.Errorf("service/env not filled from config: %q/%q", gotBody.Service, gotBody.Env)
	}
	if gotBody.Timestamp.IsZero() {
		t.Error("timestamp not auto-filled (server requires it)")
	}
	if gotBody.Metadata["subtype"] != "panic" {
		t.Errorf("subtype = %v, want panic", gotBody.Metadata["subtype"])
	}
}

func TestPostSignalNoOpWhenUninitialized(t *testing.T) {
	resetForTest()
	if err := PostSignal(context.Background(), Signal{Type: "runtime", Source: "go-sdk", Reason: "x"}); err != nil {
		t.Fatalf("PostSignal with no SDK should be a no-op, got %v", err)
	}
}

func TestSignalURLDerivation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"bare host", Config{IngestURL: "http://localhost:8080"}, "http://localhost:8080/v1/signals"},
		{"events path", Config{IngestURL: "http://localhost:8080/v1/events"}, "http://localhost:8080/v1/signals"},
		{"trailing slash", Config{IngestURL: "http://localhost:8080/"}, "http://localhost:8080/v1/signals"},
		{"preserve query", Config{IngestURL: "http://h/v1/events?x=1"}, "http://h/v1/signals?x=1"},
		{"override wins with empty ingest", Config{SignalURL: "http://other/v1/signals"}, "http://other/v1/signals"},
		{"empty both", Config{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := signalURL(tc.cfg); got != tc.want {
				t.Errorf("signalURL = %q, want %q", got, tc.want)
			}
		})
	}
}
