package waylog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	"github.com/sssmaran/WaylogCLI/pkg/event"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
	"github.com/sssmaran/WaylogCLI/pkg/transport"
)

// partialTransport succeeds for the first N events in a batch, then fails.
type partialTransport struct {
	succeedN int
}

func (t *partialTransport) Send(_ context.Context, batch []event.WideEvent) (int, error) {
	if t.succeedN >= len(batch) {
		return len(batch), nil
	}
	return t.succeedN, errors.New("partial failure")
}

func (t *partialTransport) Close(_ context.Context) error { return nil }

type codedErr struct {
	code string
	msg  string
}

func (e codedErr) Error() string { return e.msg }
func (e codedErr) Code() string  { return e.code }

func newTestClient(t *testing.T) (*waylog.Client, *transport.InMemoryTransport) {
	t.Helper()
	mem := transport.NewInMemoryTransport()
	client, err := waylog.New(waylog.Config{
		Service:   "checkout-service",
		Env:       "test",
		Transport: mem,
		ErrorClassifier: func(err error) string {
			if err == nil {
				return ""
			}
			if c, ok := err.(interface{ Code() string }); ok {
				return c.Code()
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, mem
}

func flushAndEvents(t *testing.T, client *waylog.Client, mem *transport.InMemoryTransport) []event.WideEvent {
	t.Helper()
	_ = client.Close(context.Background())
	return mem.Events()
}

func TestSuccessEvent(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = waylog.WithUser(r.Context(), waylog.User{ID: "u1"})
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventName != "checkout-service.request" {
		t.Fatalf("unexpected event name: %s", ev.EventName)
	}
	if !ev.Outcome.Success {
		t.Fatalf("expected success")
	}
	if ev.Outcome.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", ev.Outcome.StatusCode)
	}
	if ev.Error != nil {
		t.Fatalf("unexpected error context")
	}
	if ev.User.ID != "u1" {
		t.Fatalf("expected user id u1, got %s", ev.User.ID)
	}
	if ev.Request.TraceID == "" || ev.Request.SpanID == "" {
		t.Fatalf("expected trace/span ids")
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestErrorPreserves4xx(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client.Error(ctx, codedErr{code: "BAD", msg: "bad"})
		w.WriteHeader(http.StatusBadRequest)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.EventName != "checkout-service.error" {
		t.Fatalf("unexpected event name: %s", ev.EventName)
	}
	if ev.Outcome.Success {
		t.Fatalf("expected failure")
	}
	if ev.Outcome.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", ev.Outcome.StatusCode)
	}
	if ev.Error == nil || ev.Error.Code != "BAD" {
		t.Fatalf("expected error code BAD")
	}
}

func TestErrorUpgradesTo500(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client.Error(ctx, errors.New("boom"))
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.Outcome.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", ev.Outcome.StatusCode)
	}
	if ev.Outcome.Success {
		t.Fatalf("expected failure")
	}
}

func TestPanicMarkedAsFailureAndRePanics(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	recovered := false
	func() {
		defer func() {
			if recover() != nil {
				recovered = true
			}
		}()
		h.ServeHTTP(rec, req)
	}()
	if !recovered {
		t.Fatalf("expected panic to be re-thrown by middleware")
	}

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventName != "checkout-service.error" {
		t.Fatalf("unexpected event name: %s", ev.EventName)
	}
	if ev.Outcome.Success {
		t.Fatalf("expected failure event")
	}
	if ev.Outcome.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", ev.Outcome.StatusCode)
	}
	if ev.Error == nil || !strings.Contains(ev.Error.Message, "panic: boom") {
		t.Fatalf("expected panic error message, got %+v", ev.Error)
	}
}

func TestSingleEmission(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client.RequestEnd(ctx)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestRequestEndWithoutStateDrops(t *testing.T) {
	client, mem := newTestClient(t)
	client.RequestEnd(context.Background())
	events := flushAndEvents(t, client, mem)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
}

func TestErrorWithoutStateDrops(t *testing.T) {
	client, mem := newTestClient(t)
	client.Error(context.Background(), errors.New("boom"))
	events := flushAndEvents(t, client, mem)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
}

func TestTraceparentParsing(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.Request.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected trace id: %s", ev.Request.TraceID)
	}
	if ev.Request.ParentSpanID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("unexpected parent span id: %s", ev.Request.ParentSpanID)
	}
	if ev.Request.SpanID == "" || ev.Request.SpanID == ev.Request.ParentSpanID {
		t.Fatalf("expected new span id")
	}
}

func TestLegacyHeadersNormalized(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("x-trace-id", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	req.Header.Set("x-span-id", "BBBBBBBBBBBBBBBB")
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.Request.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected trace id: %s", ev.Request.TraceID)
	}
	if ev.Request.ParentSpanID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("unexpected parent span id: %s", ev.Request.ParentSpanID)
	}
}

func TestRequestIDFallback(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("x-request-id", "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.Request.TraceID != "cccccccccccccccccccccccccccccccc" {
		t.Fatalf("unexpected trace id: %s", ev.Request.TraceID)
	}
}

func TestCallerServiceCaptured(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("x-waylog-service", "inventory-service")
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.System.CallerService != "inventory-service" {
		t.Fatalf("unexpected caller service: %s", ev.System.CallerService)
	}
}

func TestDownstreamServiceCapturedAndHeadersInjected(t *testing.T) {
	client, mem := newTestClient(t)

	rt := &recordingRoundTripper{}
	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		outbound := &http.Client{Transport: wayloghttp.WrapTransport(rt, "payment-service")}
		outReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example", nil)
		_, _ = outbound.Do(outReq)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	ev := events[0]
	if ev.System.DownstreamService != "payment-service" {
		t.Fatalf("unexpected downstream service: %s", ev.System.DownstreamService)
	}

	headers := rt.Headers()
	if headers.Get("traceparent") == "" {
		t.Fatalf("expected traceparent header")
	}
	if headers.Get("x-waylog-service") != "checkout-service" {
		t.Fatalf("unexpected x-waylog-service: %s", headers.Get("x-waylog-service"))
	}
}

func TestWrapTransportNoStateNoHeaders(t *testing.T) {
	rt := &recordingRoundTripper{}
	wrapped := wayloghttp.WrapTransport(rt, "payment-service")

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example", nil)
	_, _ = wrapped.RoundTrip(req)

	headers := rt.Headers()
	if headers.Get("traceparent") != "" {
		t.Fatalf("expected no traceparent header")
	}
	if headers.Get("x-waylog-service") != "" {
		t.Fatalf("expected no x-waylog-service header")
	}
}

func TestMiddlewareCapturesHTTPMethod(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/users/123", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Request.HTTPMethod != http.MethodPost {
		t.Fatalf("http_method = %q, want %q", events[0].Request.HTTPMethod, http.MethodPost)
	}
}

func TestWithRouteTemplateIsEmitted(t *testing.T) {
	client, mem := newTestClient(t)

	h := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := waylog.WithRouteTemplate(r.Context(), "/users/{id}")
		r = r.WithContext(ctx)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Request.RouteTemplate != "/users/{id}" {
		t.Fatalf("route_template = %q, want %q", events[0].Request.RouteTemplate, "/users/{id}")
	}
}

func TestExplicitRouteTemplateNotOverwrittenByPattern(t *testing.T) {
	client, mem := newTestClient(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := waylog.WithRouteTemplate(r.Context(), "/explicit/{id}")
		r = r.WithContext(ctx)
		w.WriteHeader(http.StatusOK)
	})
	h := wayloghttp.MiddlewareWithClient(client)(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Request.RouteTemplate != "/explicit/{id}" {
		t.Fatalf("route_template = %q, want %q", events[0].Request.RouteTemplate, "/explicit/{id}")
	}
}

func TestPatternNotCapturedWithoutExplicitTemplate(t *testing.T) {
	client, mem := newTestClient(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := wayloghttp.MiddlewareWithClient(client)(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/orders/123", nil)
	h.ServeHTTP(rec, req)

	events := flushAndEvents(t, client, mem)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Request.RouteTemplate != "" {
		t.Fatalf("route_template = %q, want empty", events[0].Request.RouteTemplate)
	}
}

type recordingRoundTripper struct {
	mu      sync.Mutex
	headers http.Header
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.headers = req.Header.Clone()
	r.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString("ok")),
		Header:     make(http.Header),
	}, nil
}

func (r *recordingRoundTripper) Headers() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.headers == nil {
		return make(http.Header)
	}
	return r.headers.Clone()
}

func TestHTTPTransportEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var received []event.WideEvent

	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev event.WideEvent
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &ev); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ingest.Close()

	client, err := waylog.New(waylog.Config{
		Service:   "e2e-service",
		Env:       "test",
		IngestURL: ingest.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	handler := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = waylog.WithUser(r.Context(), waylog.User{ID: "test-user"})
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)

	_ = client.Close(context.Background())

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	ev := received[0]
	if ev.System.Service != "e2e-service" {
		t.Fatalf("service = %q, want e2e-service", ev.System.Service)
	}
	if ev.Request.TraceID == "" {
		t.Fatal("expected non-empty trace_id")
	}
	if ev.Outcome.StatusCode != 200 {
		t.Fatalf("status_code = %d, want 200", ev.Outcome.StatusCode)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestClassifyErrorFallbackHTTPCode(t *testing.T) {
	mem := transport.NewInMemoryTransport()
	client, err := waylog.New(waylog.Config{
		Service:   "test-service",
		Env:       "test",
		Transport: mem,
		// No ErrorClassifier — should fall back to HTTP_<status>
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	handler := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client.Error(ctx, errors.New("boom"))
		w.WriteHeader(http.StatusBadGateway)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	_ = client.Close(context.Background())
	events := mem.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error == nil {
		t.Fatal("expected error context")
	}
	if events[0].Error.Code != "HTTP_502" {
		t.Fatalf("error code = %q, want HTTP_502", events[0].Error.Code)
	}
}

func TestClassifyErrorNilErrWithStatusCode(t *testing.T) {
	mem := transport.NewInMemoryTransport()
	client, err := waylog.New(waylog.Config{
		Service:   "test-service",
		Env:       "test",
		Transport: mem,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	handler := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	_ = client.Close(context.Background())
	events := mem.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// 502 < 500 is false, so success=false. err is nil but classifier should produce HTTP_502
	if events[0].Error == nil {
		t.Fatal("expected error context")
	}
	if events[0].Error.Code != "HTTP_502" {
		t.Fatalf("error code = %q, want HTTP_502", events[0].Error.Code)
	}
}

func TestPartialSendStatsAccounting(t *testing.T) {
	pt := &partialTransport{succeedN: 2}
	client, err := waylog.New(waylog.Config{
		Service:   "stats-service",
		Env:       "test",
		Transport: pt,
		BatchSize: 5, // large enough to batch all 3 events
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	// Send 3 events through middleware — transport will succeed on 2, fail on 1
	for i := 0; i < 3; i++ {
		handler := wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = waylog.WithUser(r.Context(), waylog.User{ID: "u1"})
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.ServeHTTP(rec, req)
	}

	_ = client.Close(context.Background())

	stats := client.Stats()
	if stats.EventsEmitted != 2 {
		t.Fatalf("EventsEmitted = %d, want 2", stats.EventsEmitted)
	}
	if stats.EventsDropped != 1 {
		t.Fatalf("EventsDropped = %d, want 1", stats.EventsDropped)
	}
	if stats.TransportErrors != 1 {
		t.Fatalf("TransportErrors = %d, want 1", stats.TransportErrors)
	}
}

func TestTraceContextNormalization(t *testing.T) {
	traceID := trace.NewTraceID()
	spanID := trace.NewSpanID()
	if traceID == "" || spanID == "" {
		t.Fatalf("expected trace/span ids")
	}
}
