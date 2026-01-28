package trace

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

const (
	HeaderTraceID = "X-Trace-Id"
	HeaderSpanID  = "X-Span-Id"
)

type TraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
}

type ctxKey struct{}

func FromContext(ctx context.Context) (TraceContext, bool) {
	tc, ok := ctx.Value(ctxKey{}).(TraceContext)
	return tc, ok
}

func WithContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

func NewRoot() TraceContext {
	return TraceContext{
		TraceID: uuid.NewString(),
		SpanID:  uuid.NewString(),
	}
}

func NewChild(traceID, parentSpanID string) TraceContext {
	return TraceContext{
		TraceID:      traceID,
		SpanID:       uuid.NewString(),
		ParentSpanID: parentSpanID,
	}
}

func ExtractOrCreate(r *http.Request) (context.Context, TraceContext) {
	traceID := r.Header.Get(HeaderTraceID)
	parentSpanID := r.Header.Get(HeaderSpanID)

	if traceID == "" {
		tc := NewRoot()
		return WithContext(r.Context(), tc), tc
	}

	tc := TraceContext{
		TraceID:      traceID,
		SpanID:       uuid.NewString(),
		ParentSpanID: parentSpanID,
	}
	return WithContext(r.Context(), tc), tc
}

func Inject(req *http.Request, tc TraceContext) {
	if tc.TraceID != "" {
		req.Header.Set(HeaderTraceID, tc.TraceID)
	}
	if tc.SpanID != "" {
		req.Header.Set(HeaderSpanID, tc.SpanID)
	}
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := ExtractOrCreate(r)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
