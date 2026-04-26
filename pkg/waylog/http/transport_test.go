package wayloghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

func TestRoundTripperInjectsTraceparent(t *testing.T) {
	deadline, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waylogv2.Shutdown(deadline)

	buf := &bytes.Buffer{}
	if err := waylogv2.Init(waylogv2.Config{
		Service: "checkout",
		Env:     "test",
		Output:  buf,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var receivedTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "fedcba9876543210",
	})

	err := waylogv2.StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		client := &http.Client{Transport: NewTransport(http.DefaultTransport, "payment")}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/charge", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	})
	if err != nil {
		t.Fatalf("StepVoid: %v", err)
	}
	if _, err := waylogv2.Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if receivedTraceparent == "" {
		t.Fatal("expected traceparent injected")
	}
	wantPrefix := "00-0123456789abcdef0123456789abcdef-"
	if len(receivedTraceparent) != 55 || receivedTraceparent[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("traceparent=%q want prefix %q and W3C shape", receivedTraceparent, wantPrefix)
	}

	ev := lastHTTPTransportEvent(t, buf)
	if len(ev.Steps) != 1 {
		t.Fatalf("want 1 step, got %+v", ev.Steps)
	}
	step := ev.Steps[0]
	if step.SpanID == "" {
		t.Fatalf("step span_id missing: %+v", step)
	}
	if step.Downstream == nil {
		t.Fatalf("downstream missing: %+v", step)
	}
	if step.Downstream.Service != "payment" || step.Downstream.Endpoint != "POST /charge" || step.Downstream.Kind != "rpc" {
		t.Fatalf("downstream wrong: %+v", step.Downstream)
	}
}

func TestRoundTripperNoActiveStepStillInjectsButDoesNotRecordStepLinkage(t *testing.T) {
	deadline, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waylogv2.Shutdown(deadline)

	buf := &bytes.Buffer{}
	if err := waylogv2.Init(waylogv2.Config{
		Service: "checkout",
		Env:     "test",
		Output:  buf,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var receivedTraceparent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SpanID:  "bbbbbbbbbbbbbbbb",
	})

	client := &http.Client{Transport: NewTransport(http.DefaultTransport, "payment")}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/health", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := waylogv2.Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if receivedTraceparent == "" {
		t.Fatal("expected traceparent injected")
	}
	ev := lastHTTPTransportEvent(t, buf)
	if len(ev.Steps) != 0 {
		t.Fatalf("no active step should mean no recorded step linkage: %+v", ev.Steps)
	}
}

func lastHTTPTransportEvent(t *testing.T, buf *bytes.Buffer) *eventv2.Event {
	t.Helper()
	raw := bytes.TrimSpace(buf.Bytes())
	if len(raw) == 0 {
		t.Fatal("no event emitted")
	}
	lines := bytes.Split(raw, []byte{'\n'})
	var ev eventv2.Event
	if err := json.Unmarshal(lines[len(lines)-1], &ev); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	return &ev
}
