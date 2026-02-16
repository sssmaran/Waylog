package http

import (
	"fmt"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	"github.com/sssmaran/WaylogCLI/pkg/waylog/trace"
)

const (
	headerTraceparent   = "traceparent"
	headerTraceID       = "x-trace-id"
	headerSpanID        = "x-span-id"
	headerRequestID     = "x-request-id"
	headerWaylogService = "x-waylog-service"
)

func Middleware(next http.Handler) http.Handler {
	return MiddlewareWithClient(nil)(next)
}

func MiddlewareWithClient(client *waylog.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := r.Header.Get(headerWaylogService)
			if caller == "" {
				caller = "external"
			}

			traceContext := extractTraceContext(r)

			serviceName := ""
			if client != nil {
				serviceName = client.ServiceName()
			} else {
				serviceName = waylog.DefaultServiceName()
			}

			reqState := waylog.NewRequestState(time.Now(), http.StatusOK, caller, serviceName)
			ctx := trace.WithContext(r.Context(), traceContext)
			ctx = waylog.WithRequestState(ctx, reqState)

			sw := &statusWriter{
				ResponseWriter: w,
				status:         http.StatusOK,
				state:          reqState,
			}

			defer func() {
				if recovered := recover(); recovered != nil {
					panicErr := fmt.Errorf("panic: %v", recovered)
					if client != nil {
						client.Error(ctx, panicErr)
						client.RequestEnd(ctx)
					} else {
						waylog.Error(ctx, panicErr)
						waylog.RequestEnd(ctx)
					}
					panic(recovered)
				}

				if client != nil {
					client.RequestEnd(ctx)
					return
				}
				waylog.RequestEnd(ctx)
			}()

			next.ServeHTTP(sw, r.WithContext(ctx))
		})
	}
}

func extractTraceContext(r *http.Request) trace.TraceContext {
	if header := r.Header.Get(headerTraceparent); header != "" {
		if traceID, parentSpanID, flags, ok := trace.ParseTraceparent(header); ok {
			return trace.TraceContext{
				TraceID:      traceID,
				SpanID:       trace.NewSpanID(),
				ParentSpanID: parentSpanID,
				Flags:        flags,
			}
		}
	}

	traceID, _ := trace.NormalizeTraceID(r.Header.Get(headerTraceID))
	parentSpanID, _ := trace.NormalizeSpanID(r.Header.Get(headerSpanID))
	if traceID == "" {
		traceID, _ = trace.NormalizeTraceID(r.Header.Get(headerRequestID))
	}
	if traceID == "" {
		traceID = trace.NewTraceID()
	}

	return trace.TraceContext{
		TraceID:      traceID,
		SpanID:       trace.NewSpanID(),
		ParentSpanID: parentSpanID,
		Flags:        "01",
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	state       *waylog.RequestState
}

func (w *statusWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.wroteHeader = true
	if w.state != nil {
		w.state.SetStatus(statusCode)
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
