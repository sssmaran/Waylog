package wayloghttp

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/trace"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const (
	headerTraceparent = "traceparent"
	headerTraceID     = "x-trace-id"
	headerSpanID      = "x-span-id"
	headerRequestID   = "x-request-id"
)

// HTTP wraps a net/http handler so each request produces exactly one final
// Waylog v2.0 wide event following the middleware lifecycle rules.
func HTTP(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeHTTP(w, r, routePattern(r), next.ServeHTTP)
	})
}

// ServeHTTP applies the shared Waylog v2 HTTP lifecycle to a request using
// the provided route template and next callback. Framework adapters should
// call this rather than reimplementing panic/cancel/watchdog precedence.
func ServeHTTP(w http.ResponseWriter, r *http.Request, route string, next func(http.ResponseWriter, *http.Request)) {
	if next == nil {
		next = http.NotFoundHandler().ServeHTTP
	}

	traceID, spanID, parentSpanID := resolveTraceContext(r)
	ctx := waylogv2.Begin(r.Context(), waylogv2.BeginOptions{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
	})

	if route == "" {
		route = routePattern(r)
	}
	waylogv2.SetField(ctx, "http", waylogv2.F{
		"method": r.Method,
		"route":  route,
		"status": http.StatusOK,
	})

	sw := wrapResponseWriter(w, ctx)
	var sealed atomic.Bool

	deliver := func(kind lifecycleKind) {
		if !sealed.CompareAndSwap(false, true) {
			_, _ = waylogv2.Finalize(ctx)
			return
		}
		switch kind {
		case lifecycleTimeout:
			_, _ = waylogv2.FinalizeTimeout(ctx)
		case lifecyclePanic:
			_, _ = waylogv2.FinalizePanic(ctx)
		case lifecycleAborted:
			_, _ = waylogv2.FinalizeAborted(ctx)
		default:
			_, _ = waylogv2.Finalize(ctx)
		}
	}

	if d := waylogv2.MaxRequestAge(); d > 0 {
		timer := time.AfterFunc(d, func() {
			deliver(lifecycleTimeout)
		})
		defer timer.Stop()
	}

	defer func() {
		if rec := recover(); rec != nil {
			if !sw.WroteHeader() {
				sw.WriteHeader(http.StatusInternalServerError)
			}
			deliver(lifecyclePanic)
		}
	}()

	next(sw, r.WithContext(ctx))

	if ctx.Err() != nil {
		deliver(lifecycleAborted)
		return
	}

	deliver(lifecycleNormal)
}

type lifecycleKind uint8

const (
	lifecycleNormal lifecycleKind = iota
	lifecyclePanic
	lifecycleAborted
	lifecycleTimeout
)

func resolveTraceContext(r *http.Request) (traceID, spanID, parentSpanID string) {
	if header := r.Header.Get(headerTraceparent); header != "" {
		if t, parent, _, ok := trace.ParseTraceparent(header); ok {
			return t, trace.NewSpanID(), parent
		}
	}

	traceID, _ = trace.NormalizeTraceID(r.Header.Get(headerTraceID))
	parentSpanID, _ = trace.NormalizeSpanID(r.Header.Get(headerSpanID))
	if traceID == "" {
		traceID, _ = trace.NormalizeTraceID(r.Header.Get(headerRequestID))
	}
	if traceID == "" {
		traceID = trace.NewTraceID()
	}
	return traceID, trace.NewSpanID(), parentSpanID
}

func routePattern(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

type statusWriter struct {
	http.ResponseWriter
	ctx         context.Context
	status      int
	wroteHeader bool
}

func wrapResponseWriter(w http.ResponseWriter, ctx context.Context) *statusWriter {
	return &statusWriter{
		ResponseWriter: w,
		ctx:            ctx,
		status:         http.StatusOK,
	}
}

func (w *statusWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *statusWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.wroteHeader = true
	waylogv2.SetHTTPStatus(w.ctx, statusCode)
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *statusWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *statusWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) WroteHeader() bool {
	return w.wroteHeader
}
