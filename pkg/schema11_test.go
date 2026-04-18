package waylog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
)

// Schema 1.1 SDK accessors — confirm new fields flow from context → emitted WideEvent.
// All accessors are additive: a request that uses none of them produces a 1.0-shaped event.

func TestSchema11_ErrorReasonAndPath_PopulatesErrorContext(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		waylog.WithErrorReason(ctx, "downstream payment gateway returned 502")
		waylog.WithErrorPath(ctx, "https://runbooks.example.com/payments-502")
		waylog.Error(ctx, errors.New("payment gateway down"))
		w.WriteHeader(http.StatusBadGateway)
	}))

	req := httptest.NewRequest(http.MethodPost, "/checkout", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Error == nil {
		t.Fatal("expected error context, got nil")
	}
	if got, want := ev.Error.Reason, "downstream payment gateway returned 502"; got != want {
		t.Errorf("error.reason: got %q, want %q", got, want)
	}
	if got, want := ev.Error.Path, "https://runbooks.example.com/payments-502"; got != want {
		t.Errorf("error.path: got %q, want %q", got, want)
	}
}

func TestSchema11_ErrorTriageFields_OmittedOnSuccess(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		waylog.WithErrorReason(ctx, "should never appear: success path")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Errorf("success event should have no error context, got %+v", events[0].Error)
	}
}

func TestSchema11_ParentRequestID(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		waylog.WithParentRequestID(r.Context(), "trace-aaa-job-triggering-this")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/work", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if got, want := events[0].ParentRequestID, "trace-aaa-job-triggering-this"; got != want {
		t.Errorf("parent_request_id: got %q, want %q", got, want)
	}
}

func TestSchema11_Metadata(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		waylog.WithMetadataKey(ctx, "tenant_id", "acme-corp")
		waylog.WithMetadataKey(ctx, "cart_total_cents", 4299)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/cart", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	md := events[0].Metadata
	if md == nil {
		t.Fatal("expected metadata map, got nil")
	}
	if got, want := md["tenant_id"], "acme-corp"; got != want {
		t.Errorf("metadata[tenant_id]: got %v, want %q", got, want)
	}
	if got, want := md["cart_total_cents"], 4299; got != want {
		t.Errorf("metadata[cart_total_cents]: got %v, want %d", got, want)
	}
}

func TestSchema11_RetryAndAttempt(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		waylog.WithAttempt(ctx, 2)
		waylog.WithRetry(ctx, waylog.Retry{Of: 3, PreviousAttemptID: "prev-span-id"})
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/charge", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if got, want := ev.Request.Attempt, 2; got != want {
		t.Errorf("request.attempt: got %d, want %d", got, want)
	}
	if ev.Retry == nil {
		t.Fatal("expected retry context, got nil")
	}
	if got, want := ev.Retry.Of, 3; got != want {
		t.Errorf("retry.of: got %d, want %d", got, want)
	}
	if got, want := ev.Retry.PreviousAttemptID, "prev-span-id"; got != want {
		t.Errorf("retry.previous_attempt_id: got %q, want %q", got, want)
	}
}

func TestSchema11_NoAccessorsCalled_ProducesCleanEvent(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.ParentRequestID != "" {
		t.Errorf("parent_request_id should be empty by default, got %q", ev.ParentRequestID)
	}
	if ev.Metadata != nil {
		t.Errorf("metadata should be nil by default, got %+v", ev.Metadata)
	}
	if ev.Retry != nil {
		t.Errorf("retry should be nil by default, got %+v", ev.Retry)
	}
	if ev.Request.Attempt != 0 {
		t.Errorf("attempt should be 0 by default, got %d", ev.Request.Attempt)
	}
}

func TestSchema11_AccessorsNoOpWithoutRequestState(t *testing.T) {
	// Calling accessors outside a request context must not panic and must not error.
	ctx := context.Background()
	waylog.WithErrorReason(ctx, "should be silently ignored")
	waylog.WithErrorPath(ctx, "should be silently ignored")
	waylog.WithParentRequestID(ctx, "should be silently ignored")
	waylog.WithMetadataKey(ctx, "k", "v")
	waylog.WithAttempt(ctx, 5)
	waylog.WithRetry(ctx, waylog.Retry{Of: 5})
}
