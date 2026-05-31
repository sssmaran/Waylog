package waylogv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// signalRecorder is an httptest server that decodes posted signals onto a channel.
func signalRecorder(t *testing.T) (*httptest.Server, chan Signal) {
	t.Helper()
	ch := make(chan Signal, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/signals" {
			var sig Signal
			_ = json.NewDecoder(r.Body).Decode(&sig)
			ch <- sig
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func TestSafeGoRecoversPanicAndPostsSignal(t *testing.T) {
	t.Cleanup(resetForTest)
	srv, ch := signalRecorder(t)
	if err := Init(Config{Service: "checkout", Env: "demo", IngestURL: srv.URL, APIKey: "k", EnableRuntimeHooks: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	SafeGo(func() { panic("boom") })

	select {
	case sig := <-ch:
		if sig.Type != "runtime" || sig.Source != "go-sdk" {
			t.Errorf("type/source = %q/%q, want runtime/go-sdk", sig.Type, sig.Source)
		}
		if sig.Service != "checkout" || sig.Env != "demo" {
			t.Errorf("service/env = %q/%q, want checkout/demo", sig.Service, sig.Env)
		}
		if sig.Metadata["subtype"] != "panic" {
			t.Errorf("subtype = %v, want panic", sig.Metadata["subtype"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SafeGo panic signal")
	}
}

func TestSafeGoNoSignalWhenHooksDisabled(t *testing.T) {
	t.Cleanup(resetForTest)
	srv, ch := signalRecorder(t)
	if err := Init(Config{Service: "checkout", Env: "demo", IngestURL: srv.URL, APIKey: "k"}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	SafeGo(func() { panic("boom") })

	select {
	case sig := <-ch:
		t.Fatalf("unexpected signal with hooks disabled: %+v", sig)
	case <-time.After(250 * time.Millisecond):
	}
}
