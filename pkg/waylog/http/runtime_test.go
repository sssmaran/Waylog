package wayloghttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// TestPanicPostsRuntimeSignal verifies the real ingest path: a panic inside an
// instrumented handler is recovered by the adapter, returns 500, and (with
// runtime hooks enabled) posts a "runtime" signal with subtype=panic carrying
// the SDK's service/env so it correlates with the incident.
func TestPanicPostsRuntimeSignal(t *testing.T) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = waylogv2.Shutdown(ctx)
	})

	signals := make(chan waylogv2.Signal, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/signals" {
			var sig waylogv2.Signal
			_ = json.NewDecoder(r.Body).Decode(&sig)
			signals <- sig
			w.WriteHeader(http.StatusCreated)
			return
		}
		// /v1/events
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	defer srv.Close()

	if err := waylogv2.Init(waylogv2.Config{
		Service:            "checkout",
		Env:                "demo",
		IngestURL:          srv.URL,
		APIKey:             "k",
		EnableRuntimeHooks: true,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	h := wayloghttp.HTTP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler boom")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/checkout", nil)
	h.ServeHTTP(rec, req) // must not propagate the panic

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	select {
	case sig := <-signals:
		if sig.Type != "runtime" || sig.Source != "go-sdk" {
			t.Errorf("type/source = %q/%q, want runtime/go-sdk", sig.Type, sig.Source)
		}
		if sig.Service != "checkout" || sig.Env != "demo" {
			t.Errorf("service/env = %q/%q, want checkout/demo", sig.Service, sig.Env)
		}
		if sig.Metadata["subtype"] != "panic" {
			t.Errorf("subtype = %v, want panic", sig.Metadata["subtype"])
		}
		if !strings.Contains(sig.Reason, "panic") {
			t.Errorf("reason = %q, want it to mention the panic", sig.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime signal from panic")
	}
}
