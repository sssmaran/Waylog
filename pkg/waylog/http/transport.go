package wayloghttp

import (
	"net/http"

	"github.com/sssmaran/WaylogCLI/pkg/trace"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// Transport injects W3C traceparent on outgoing HTTP calls and records the
// client span on the innermost active Waylog step.
type Transport struct {
	Base           http.RoundTripper
	DownstreamHint string
}

// NewTransport returns a RoundTripper that propagates W3C trace context and
// records downstream linkage on the active step.
func NewTransport(base http.RoundTripper, downstreamHint string) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{Base: base, DownstreamHint: downstreamHint}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	traceID := waylogv2.TraceID(req.Context())
	parentSpan := waylogv2.SpanID(req.Context())
	if traceID == "" || parentSpan == "" {
		return base.RoundTrip(req)
	}

	clientSpan := trace.NewSpanID()
	waylogv2.RecordOutgoingSpan(req.Context(), clientSpan, t.DownstreamHint, req.Method+" "+req.URL.Path)

	cloned := req.Clone(req.Context())
	cloned.Header.Set(headerTraceparent, trace.FormatTraceparent(traceID, clientSpan, "01"))
	return base.RoundTrip(cloned)
}
