package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	"github.com/sssmaran/WaylogCLI/pkg/transport"
)

func validEvent() event.WideEvent {
	return event.WideEvent{
		SchemaVersion: "1.0",
		EventName:     "test-service.request",
		Timestamp:     time.Now().UTC(),
		User:          event.UserContext{ID: "u1"},
		Request:       event.RequestContext{TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		System:        event.SystemContext{Service: "test-service", Env: "test"},
		Outcome:       event.OutcomeContext{Success: true, StatusCode: 200, Kind: "http"},
		Metrics:       event.MetricsContext{LatencyMs: 10},
	}
}

func TestHTTPTransportSend(t *testing.T) {
	var received int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev event.WideEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		atomic.AddInt64(&received, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	ht, err := transport.NewHTTPTransport(ts.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	batch := []event.WideEvent{validEvent(), validEvent(), validEvent()}
	sent, err := ht.Send(context.Background(), batch)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent != 3 {
		t.Fatalf("sent = %d, want 3", sent)
	}
	if atomic.LoadInt64(&received) != 3 {
		t.Fatalf("received = %d, want 3", atomic.LoadInt64(&received))
	}
}

func TestHTTPTransportRetry503(t *testing.T) {
	var attempts int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	ht, err := transport.NewHTTPTransport(ts.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sent, err := ht.Send(context.Background(), []event.WideEvent{validEvent()})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}
	if atomic.LoadInt64(&attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", atomic.LoadInt64(&attempts))
	}
}

func TestHTTPTransportRetryExhausted(t *testing.T) {
	var attempts int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	ht, err := transport.NewHTTPTransport(ts.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	sent, err := ht.Send(context.Background(), []event.WideEvent{validEvent()})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if sent != 0 {
		t.Fatalf("sent = %d, want 0", sent)
	}
	if atomic.LoadInt64(&attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", atomic.LoadInt64(&attempts))
	}
}

func TestHTTPTransportTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer ts.Close()

	ht, err := transport.NewHTTPTransport(ts.URL, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = ht.Send(context.Background(), []event.WideEvent{validEvent()})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestHTTPTransportConnectionRefused(t *testing.T) {
	ht, err := transport.NewHTTPTransport("http://127.0.0.1:1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = ht.Send(context.Background(), []event.WideEvent{validEvent()})
	if err == nil {
		t.Fatal("expected connection error")
	}
}

func TestHTTPTransportURLNormalization(t *testing.T) {
	var received int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			t.Errorf("path = %q, want /v1/events", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt64(&received, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	// URL already ends with /v1/events — should not double-append
	ht, err := transport.NewHTTPTransport(ts.URL+"/v1/events", 5*time.Second)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	sent, err := ht.Send(context.Background(), []event.WideEvent{validEvent()})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}

	// URL with trailing slash
	ht2, err := transport.NewHTTPTransport(ts.URL+"/", 5*time.Second)
	if err != nil {
		t.Fatalf("new trailing slash: %v", err)
	}
	sent, err = ht2.Send(context.Background(), []event.WideEvent{validEvent()})
	if err != nil {
		t.Fatalf("send trailing slash: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}
}

func TestHTTPTransportRejectsInvalidURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"missing scheme", "localhost:8080"},
		{"missing host", "http://"},
		{"with query", "http://localhost:8080?foo=bar"},
		{"with fragment", "http://localhost:8080#section"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := transport.NewHTTPTransport(tt.url, 0)
			if err == nil {
				t.Fatalf("expected error for URL %q", tt.url)
			}
		})
	}
}
