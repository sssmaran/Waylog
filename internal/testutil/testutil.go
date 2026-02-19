package testutil

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// EventOption is a function that modifies an event.
type EventOption func(*event.WideEvent)

// MakeEvent creates a WideEvent with sensible defaults and applies options.
func MakeEvent(opts ...EventOption) event.WideEvent {
	ev := event.WideEvent{
		SchemaVersion: "1.0",
		EventName:     "test-service.request",
		Timestamp:     time.Now().UTC(),
		Request: event.RequestContext{
			TraceID: "0123456789abcdef0123456789abcdef",
			SpanID:  "0123456789abcdef",
			Flow:    "checkout",
		},
		System: event.SystemContext{
			Service: "test-service",
			Env:     "test",
			Version: "1.0.0",
		},
		User: event.UserContext{
			ID:     "user-123",
			Tier:   "standard",
			Region: "us-west-2",
		},
		Outcome: event.OutcomeContext{
			Success:    true,
			StatusCode: 200,
		},
		Metrics: event.MetricsContext{
			LatencyMs: 50,
		},
	}

	for _, opt := range opts {
		opt(&ev)
	}

	return ev
}

// WithTraceID sets the trace ID.
func WithTraceID(traceID string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Request.TraceID = traceID
	}
}

// WithSpanID sets the span ID.
func WithSpanID(spanID string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Request.SpanID = spanID
	}
}

// WithParentSpanID sets the parent span ID.
func WithParentSpanID(parentSpanID string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Request.ParentSpanID = parentSpanID
	}
}

// WithService sets the service name.
func WithService(name string) EventOption {
	return func(ev *event.WideEvent) {
		ev.System.Service = name
		ev.EventName = name + ".request"
	}
}

// WithError adds an error to the event.
func WithError(code, message string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Error = &event.ErrorContext{
			Code:    code,
			Message: message,
		}
		ev.Outcome.Success = false
		ev.EventName = ev.System.Service + ".error"
	}
}

// WithStatusCode sets the status code.
func WithStatusCode(code int) EventOption {
	return func(ev *event.WideEvent) {
		ev.Outcome.StatusCode = code
		if code >= 400 {
			ev.Outcome.Success = false
		}
	}
}

// WithLatency sets the latency in milliseconds.
func WithLatency(ms int64) EventOption {
	return func(ev *event.WideEvent) {
		ev.Metrics.LatencyMs = ms
	}
}

// WithUser sets user details.
func WithUser(id, tier, region string) EventOption {
	return func(ev *event.WideEvent) {
		ev.User.ID = id
		ev.User.Tier = tier
		ev.User.Region = region
	}
}

// WithFlow sets the flow name.
func WithFlow(flow string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Request.Flow = flow
	}
}

// WithCallerService sets the caller service.
func WithCallerService(service string) EventOption {
	return func(ev *event.WideEvent) {
		ev.System.CallerService = service
	}
}

// WithDownstreamService sets the downstream service.
func WithDownstreamService(service string) EventOption {
	return func(ev *event.WideEvent) {
		ev.System.DownstreamService = service
	}
}

// WithFeatureFlags sets feature flags.
func WithFeatureFlags(flags ...string) EventOption {
	return func(ev *event.WideEvent) {
		ev.Request.FeatureFlags = flags
	}
}

// WithTimestamp sets the event timestamp.
func WithTimestamp(t time.Time) EventOption {
	return func(ev *event.WideEvent) {
		ev.Timestamp = t
	}
}

// WithVIP sets the VIP flag on the user.
func WithVIP(vip bool) EventOption {
	return func(ev *event.WideEvent) { ev.User.VIP = vip }
}

// WithEventName sets the event name directly.
func WithEventName(name string) EventOption {
	return func(ev *event.WideEvent) {
		ev.EventName = name
	}
}

// MakeGraph creates a graph with the given nodes and edges.
func MakeGraph(nodes []core.Node, edges []core.Edge) *core.Graph {
	g := core.New()
	for _, n := range nodes {
		g.AddNode(n)
	}
	for _, e := range edges {
		g.AddEdge(e)
	}
	return g
}

// MakeNode creates a node with the given ID and type.
func MakeNode(id string, nodeType core.NodeType, attr map[string]any) core.Node {
	return core.Node{
		ID:   id,
		Type: nodeType,
		Attr: attr,
	}
}
