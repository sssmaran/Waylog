package http

import (
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

func WrapTransport(rt http.RoundTripper, downstreamService string) http.RoundTripper {
	return &wrappedTransport{
		base:              rt,
		downstreamService: downstreamService,
	}
}

type wrappedTransport struct {
	base              http.RoundTripper
	downstreamService string
}

func (t *wrappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	ctx := req.Context()
	traceContext, ok := trace.FromContext(ctx)
	if !ok || traceContext.TraceID == "" || traceContext.SpanID == "" {
		traceContext = trace.TraceContext{
			TraceID: trace.NewTraceID(),
			SpanID:  trace.NewSpanID(),
			Flags:   "01",
		}
	}

	reqState, ok := waylog.RequestStateFromContext(ctx)
	if !ok || reqState == nil {
		return base.RoundTrip(req)
	}
	reqState.SetDownstream(t.downstreamService)
	serviceName := reqState.ServiceName()
	if serviceName == "" {
		serviceName = waylog.DefaultServiceName()
	}

	cloned := req.Clone(ctx)
	cloned.Header.Set(headerTraceparent, trace.FormatTraceparent(traceContext.TraceID, traceContext.SpanID, traceContext.Flags))
	if serviceName != "" {
		cloned.Header.Set(headerWaylogService, serviceName)
	}

	return base.RoundTrip(cloned)
}
