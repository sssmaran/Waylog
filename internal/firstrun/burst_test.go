package firstrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

func TestRunBurstEmitsFailingPaymentEvents(t *testing.T) {
	var events, alerts, signals int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/events":
			batch, _ := decodeEventBatch(r)
			atomic.AddInt64(&events, int64(len(batch)))
			w.WriteHeader(http.StatusAccepted)
		case "/v1/alerts":
			atomic.AddInt64(&alerts, 1)
			w.WriteHeader(http.StatusCreated)
		case "/v1/signals":
			atomic.AddInt64(&signals, 1)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Cleanup(func() { _ = waylog.Shutdown(context.Background()) })

	res, err := RunBurst(BurstConfig{
		IngestURL: srv.URL,
		WriteKey:  "demo",
		Requests:  burstTestCount,
	})
	if err != nil {
		t.Fatalf("RunBurst: %v", err)
	}
	if res.FailingEvents != burstTestCount {
		t.Fatalf("FailingEvents = %d, want %d", res.FailingEvents, burstTestCount)
	}
	if atomic.LoadInt64(&alerts) != 1 {
		t.Fatalf("alerts posted = %d, want 1", atomic.LoadInt64(&alerts))
	}
	if atomic.LoadInt64(&signals) != 1 {
		t.Fatalf("runtime signals posted = %d, want 1", atomic.LoadInt64(&signals))
	}
	if atomic.LoadInt64(&events) < burstTestCount {
		t.Fatalf("events received = %d, want >= %d", atomic.LoadInt64(&events), burstTestCount)
	}
}

// decodeEventBatch handles both NDJSON (one JSON object per line, used by the
// batch transport) and a JSON array body. Returns one entry per event.
func decodeEventBatch(r *http.Request) ([]map[string]any, error) {
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	var out []map[string]any
	for dec.More() {
		var one map[string]any
		if err := dec.Decode(&one); err != nil {
			break
		}
		out = append(out, one)
	}
	return out, nil
}
