package wayloghttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const schemaPath = "../../../docs/schema/v2.0.json"

type harness struct {
	t   *testing.T
	buf *lockedBuffer
}

func newHarness(t *testing.T, cfg waylogv2.Config) *harness {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waylogv2.Shutdown(deadline)

	buf := &lockedBuffer{}
	if cfg.Service == "" {
		cfg.Service = "checkout"
	}
	if cfg.Env == "" {
		cfg.Env = "test"
	}
	cfg.Output = buf
	if err := waylogv2.Init(cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return &harness{t: t, buf: buf}
}

func (h *harness) lastEvent() *eventv2.Event {
	h.t.Helper()
	out := h.buf.Bytes()
	if len(out) == 0 {
		return nil
	}
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte{'\n'})
	var ev eventv2.Event
	if err := json.Unmarshal(lines[len(lines)-1], &ev); err != nil {
		h.t.Fatalf("unmarshal emitted event: %v", err)
	}
	return &ev
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (h *harness) validateLast() *eventv2.Event {
	h.t.Helper()
	ev := h.lastEvent()
	if ev == nil {
		h.t.Fatal("no event emitted")
	}
	if err := eventv2.Validate(schemaPath, ev); err != nil {
		raw, _ := json.MarshalIndent(ev, "", "  ")
		h.t.Fatalf("emitted event fails schema: %v\n%s", err, raw)
	}
	return ev
}

func httpFields(t *testing.T, ev *eventv2.Event) map[string]any {
	t.Helper()
	fields, _ := ev.Fields["http"].(map[string]any)
	if fields == nil {
		t.Fatalf("fields.http missing: %+v", ev.Fields)
	}
	return fields
}

func TestHTTPMiddlewareEmitsOK(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("status=%s want ok", ev.Status)
	}
	fields := httpFields(t, ev)
	if got := fields["method"]; got != http.MethodGet {
		t.Fatalf("method=%v want %s", got, http.MethodGet)
	}
	if got := fields["route"]; got != "/health" {
		t.Fatalf("route=%v want /health", got)
	}
	if got, _ := fields["status"].(float64); got != 200 {
		t.Fatalf("status=%v want 200", fields["status"])
	}
}

func TestHTTPMiddlewareUsesPatternWhenPresent(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/checkout/123", nil)
	req.Pattern = "/checkout/{id}"
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	fields := httpFields(t, ev)
	if got := fields["route"]; got != "/checkout/{id}" {
		t.Fatalf("route=%v want pattern", got)
	}
}

func TestHTTPMiddlewareWriteWithoutHeaderRecords200(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/write", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	fields := httpFields(t, ev)
	if got, _ := fields["status"].(float64); got != 200 {
		t.Fatalf("status=%v want 200", fields["status"])
	}
}

func TestHTTPMiddlewareKeepsFirstWriteHeaderStatus(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/double-header", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	fields := httpFields(t, ev)
	if got, _ := fields["status"].(float64); got != 204 {
		t.Fatalf("fields.http.status=%v want 204", fields["status"])
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("response code=%d want 204", rr.Code)
	}
}

func TestHTTPMiddlewarePreservesTraceparent(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/trace", nil)
	req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace_id=%s want propagated trace id", ev.TraceID)
	}
	if ev.ParentSpanID != "bbbbbbbbbbbbbbbb" {
		t.Fatalf("parent_span_id=%s want propagated parent span", ev.ParentSpanID)
	}
	if ev.SpanID == "" || ev.SpanID == ev.ParentSpanID {
		t.Fatalf("span_id=%s want fresh local server span", ev.SpanID)
	}
}

func TestHTTPMiddlewareTraceFallbacks(t *testing.T) {
	cases := []struct {
		name           string
		headers        map[string]string
		wantTraceID    string
		wantParentSpan string
	}{
		{
			name: "x-trace-id and x-span-id",
			headers: map[string]string{
				"x-trace-id": "cccccccccccccccccccccccccccccccc",
				"x-span-id":  "dddddddddddddddd",
			},
			wantTraceID:    "cccccccccccccccccccccccccccccccc",
			wantParentSpan: "dddddddddddddddd",
		},
		{
			name: "x-request-id fallback",
			headers: map[string]string{
				"x-request-id": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			},
			wantTraceID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		},
		{
			name:        "fresh trace fallback",
			headers:     map[string]string{},
			wantTraceID: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, waylogv2.Config{})
			handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/trace", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			handler.ServeHTTP(rr, req)

			ev := h.validateLast()
			if tc.wantTraceID != "" && ev.TraceID != tc.wantTraceID {
				t.Fatalf("trace_id=%s want %s", ev.TraceID, tc.wantTraceID)
			}
			if tc.wantTraceID == "" && ev.TraceID == "" {
				t.Fatal("fresh trace fallback must generate a trace_id")
			}
			if tc.wantParentSpan != "" && ev.ParentSpanID != tc.wantParentSpan {
				t.Fatalf("parent_span_id=%s want %s", ev.ParentSpanID, tc.wantParentSpan)
			}
		})
	}
}

func TestHTTPMiddlewarePanicEmitsPanicAnchorAndWrites500(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "request" || ev.Anchor.ErrorCode != eventv2.CodePanic {
		t.Fatalf("panic anchor wrong: %+v", ev.Anchor)
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("response code=%d want 500", rr.Code)
	}
	fields := httpFields(t, ev)
	if got, _ := fields["status"].(float64); got != 500 {
		t.Fatalf("fields.http.status=%v want 500", fields["status"])
	}
}

func TestHTTPMiddlewarePanicAnchorsInnermostActiveStep(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = waylogv2.StepVoid(r.Context(), "db.load_cart", func(ctx context.Context) error {
			panic("boom")
		})
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic-step", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Anchor == nil || ev.Anchor.Step != "db.load_cart" || ev.Anchor.ErrorCode != eventv2.CodePanic {
		t.Fatalf("panic anchor wrong: %+v", ev.Anchor)
	}
}

func TestHTTPMiddlewarePanicDoesNotOverwriteWrittenStatus(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		panic("boom")
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic-written", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	fields := httpFields(t, ev)
	if got, _ := fields["status"].(float64); got != 204 {
		t.Fatalf("fields.http.status=%v want 204", fields["status"])
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("response code=%d want 204", rr.Code)
	}
}

func TestHTTPMiddlewareCancelEmitsAborted(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	reqCtx, cancel := context.WithCancel(context.Background())
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/abort", nil).WithContext(reqCtx)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusAborted {
		t.Fatalf("status=%s want aborted", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "request" || ev.Anchor.ErrorCode != eventv2.CodeAborted {
		t.Fatalf("aborted anchor wrong: %+v", ev.Anchor)
	}
}

func TestHTTPMiddlewareCancelKeepsExplicitFailure(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	reqCtx, cancel := context.WithCancel(context.Background())
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		waylogv2.Fail(r.Context(), waylogv2.NewError("AUTH_DENIED"))
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/abort-fail", nil).WithContext(reqCtx)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.ErrorCode != "AUTH_DENIED" {
		t.Fatalf("explicit failure must win over aborted finalize: %+v", ev.Anchor)
	}
}

func TestHTTPMiddlewareSuppressBeatsCancel(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	reqCtx, cancel := context.WithCancel(context.Background())
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		waylogv2.Suppress(r.Context())
		cancel()
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/suppress", nil).WithContext(reqCtx)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusSuppressed {
		t.Fatalf("status=%s want suppressed", ev.Status)
	}
	if ev.Anchor != nil {
		t.Fatalf("suppressed event must not carry anchor: %+v", ev.Anchor)
	}
}

func TestHTTPMiddlewareWatchdogEmitsTimeout(t *testing.T) {
	h := newHarness(t, waylogv2.Config{MaxRequestAge: 20 * time.Millisecond})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Status != eventv2.StatusTimeout {
		t.Fatalf("status=%s want timeout", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "request" || ev.Anchor.ErrorCode != eventv2.CodeTimeout {
		t.Fatalf("timeout anchor wrong: %+v", ev.Anchor)
	}
	if got := waylogv2.Stats().LateCompletionAfterEmit; got == 0 {
		t.Fatalf("LateCompletionAfterEmit=%d want > 0 after timeout-first sealing", got)
	}
}

func TestHTTPMiddlewareWatchdogUsesInnermostActiveStep(t *testing.T) {
	h := newHarness(t, waylogv2.Config{MaxRequestAge: 20 * time.Millisecond})
	handler := HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = waylogv2.StepVoid(r.Context(), "outer", func(ctx context.Context) error {
			return waylogv2.StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
				time.Sleep(60 * time.Millisecond)
				return nil
			})
		})
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow-step", nil)
	handler.ServeHTTP(rr, req)

	ev := h.validateLast()
	if ev.Anchor == nil || ev.Anchor.Step != "payment.charge" || ev.Anchor.ErrorCode != eventv2.CodeTimeout {
		t.Fatalf("timeout anchor wrong: %+v", ev.Anchor)
	}
}

func TestStatusWriterPreservesOptionalInterfaces(t *testing.T) {
	h := newHarness(t, waylogv2.Config{})
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
	stub := newFancyWriter()
	sw := wrapResponseWriter(stub, ctx)

	if _, ok := any(sw).(http.Flusher); !ok {
		t.Fatal("wrapped writer must implement http.Flusher")
	}
	if _, ok := any(sw).(http.Hijacker); !ok {
		t.Fatal("wrapped writer must implement http.Hijacker")
	}
	if _, ok := any(sw).(http.Pusher); !ok {
		t.Fatal("wrapped writer must implement http.Pusher")
	}
	if _, ok := any(sw).(io.ReaderFrom); !ok {
		t.Fatal("wrapped writer must implement io.ReaderFrom")
	}
	unwrapper, ok := any(sw).(interface{ Unwrap() http.ResponseWriter })
	if !ok || unwrapper.Unwrap() != stub {
		t.Fatal("wrapped writer must expose Unwrap()")
	}

	flusher := any(sw).(http.Flusher)
	flusher.Flush()
	if !stub.flushed {
		t.Fatal("Flush was not delegated")
	}

	pusher := any(sw).(http.Pusher)
	if err := pusher.Push("/asset.js", nil); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if stub.pushedTarget != "/asset.js" {
		t.Fatalf("push target=%q want /asset.js", stub.pushedTarget)
	}

	hijacker := any(sw).(http.Hijacker)
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	_ = conn.Close()
	if !stub.hijacked {
		t.Fatal("Hijack was not delegated")
	}

	readerFrom := any(sw).(io.ReaderFrom)
	n, err := readerFrom.ReadFrom(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != 5 || !stub.readFromCalled {
		t.Fatalf("ReadFrom delegation failed: n=%d called=%v", n, stub.readFromCalled)
	}
	if !sw.WroteHeader() || sw.status != http.StatusOK {
		t.Fatalf("ReadFrom must auto-write 200: wrote=%v status=%d", sw.WroteHeader(), sw.status)
	}

	_, _ = waylogv2.Finalize(ctx)
	_ = h.validateLast()
}

type fancyWriter struct {
	header         http.Header
	status         int
	body           bytes.Buffer
	flushed        bool
	hijacked       bool
	pushedTarget   string
	readFromCalled bool
}

func newFancyWriter() *fancyWriter {
	return &fancyWriter{header: make(http.Header)}
}

func (w *fancyWriter) Header() http.Header {
	return w.header
}

func (w *fancyWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *fancyWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *fancyWriter) Flush() {
	w.flushed = true
}

func (w *fancyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	server, client := net.Pipe()
	_ = client.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))
	return server, rw, nil
}

func (w *fancyWriter) Push(target string, _ *http.PushOptions) error {
	w.pushedTarget = target
	return nil
}

func (w *fancyWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFromCalled = true
	return io.Copy(&w.body, r)
}
