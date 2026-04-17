package build

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type Builder struct{}

type BuildResult struct {
	Graph *core.Graph
	Span  *tracestore.SpanRecord
}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Build(ev event.WideEvent) *core.Graph {
	return b.BuildResult(ev).Graph
}

func (b *Builder) BuildResult(ev event.WideEvent) BuildResult {
	g := core.New()
	errID := ""
	isRoot := ev.Request.SpanID != "" && ev.Request.ParentSpanID == ""
	var span *tracestore.SpanRecord

	// --------------------
	// Request node
	// --------------------
	reqID := core.ID("request", ev.Request.TraceID)
	req := core.Node{
		ID:   reqID,
		Type: core.NodeRequest,
		Attr: map[string]any{
			"event_name":     ev.EventName,
			"trace_id":       ev.Request.TraceID,
			"flow":           ev.Request.Flow,
			"latency_ms":     ev.Metrics.LatencyMs,
			"success":        ev.Outcome.Success,
			"status_code":    ev.Outcome.StatusCode,
			"service":        ev.System.Service,
			"is_root":        isRoot,
			"http_method":    ev.Request.HTTPMethod,
			"route_template": ev.Request.RouteTemplate,
			"version":        ev.System.Version,
			"user_id":        ev.User.ID,
			"user_tier":      ev.User.Tier,
			"user_region":    ev.User.Region,
			"user_vip":       ev.User.VIP,
			"feature_flags":  append([]string(nil), ev.Request.FeatureFlags...),
		},
	}
	if ev.Error != nil {
		req.Attr["error_code"] = ev.Error.Code
	}
	touch(&req, ev.Timestamp)
	g.AddNode(req)

	// --------------------
	// Service node
	// --------------------
	svcID := core.ID(
		"service",
		ev.System.Service,
		ev.System.Env,
	)
	svc := core.Node{
		ID:   svcID,
		Type: core.NodeService,
		Attr: map[string]any{
			"name":          ev.System.Service,
			"env":           ev.System.Env,
			"version":       ev.System.Version,
			"deployment_id": ev.System.DeploymentID,
		},
	}
	touch(&svc, ev.Timestamp)
	g.AddNode(svc)

	g.AddEdge(core.Edge{
		From: reqID,
		To:   svcID,
		Type: core.EdgeHandledBy,
	})

	// --------------------
	// Service-to-service call edge
	// --------------------
	if ev.System.CallerService != "" {
		callerID := core.ID("service", ev.System.CallerService, ev.System.Env)

		// Ensure caller service node exists
		caller := core.Node{
			ID:   callerID,
			Type: core.NodeService,
			Attr: map[string]any{
				"env": ev.System.Env,
			},
		}
		touch(&caller, ev.Timestamp)
		g.AddNode(caller)

		// caller_service -> calls -> service
		g.AddEdge(core.Edge{
			From: callerID,
			To:   svcID,
			Type: core.EdgeCalls,
		})
	}
	// --------------------
	// Downstream service dependency
	// --------------------
	if ev.System.DownstreamService != "" {
		downID := core.ID("service", ev.System.DownstreamService, ev.System.Env)

		down := core.Node{
			ID:   downID,
			Type: core.NodeService,
			Attr: map[string]any{
				"env": ev.System.Env,
			},
		}
		touch(&down, ev.Timestamp)
		g.AddNode(down)

		g.AddEdge(core.Edge{
			From: svcID,
			To:   downID,
			Type: core.EdgeCalls,
		})
	}
	// --------------------
	// Trace store span record
	// --------------------
	if ev.Request.SpanID != "" {
		span = &tracestore.SpanRecord{
			SpanID:            ev.Request.SpanID,
			ParentSpanID:      ev.Request.ParentSpanID,
			Service:           ev.System.Service,
			EventName:         ev.EventName,
			StatusCode:        ev.Outcome.StatusCode,
			Success:           ev.Outcome.Success,
			LatencyMs:         ev.Metrics.LatencyMs,
			CallerService:     ev.System.CallerService,
			DownstreamService: ev.System.DownstreamService,
			Timestamp:         ev.Timestamp,
			HTTPMethod:        ev.Request.HTTPMethod,
			RouteTemplate:     ev.Request.RouteTemplate,
		}
		if ev.Error != nil {
			span.ErrorCode = ev.Error.Code
			span.ErrorMessage = ev.Error.Message
		}
	}

	// --------------------
	// Error node
	// --------------------
	if ev.Error != nil {
		errID = core.ID("error", ev.Error.Code)
		errNode := core.Node{
			ID:   errID,
			Type: core.NodeError,
			Attr: map[string]any{
				"code":    ev.Error.Code,
				"message": ev.Error.Message,
				"service": ev.System.Service,
			},
		}
		touch(&errNode, ev.Timestamp)
		g.AddNode(errNode)

		g.AddEdge(core.Edge{
			From: reqID,
			To:   errID,
			Type: core.EdgeFailedWith,
		})
	}

	return BuildResult{Graph: g, Span: span}
}

func touch(n *core.Node, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if n.FirstSeen.IsZero() || ts.Before(n.FirstSeen) {
		n.FirstSeen = ts
	}
	if n.LastSeen.IsZero() || ts.After(n.LastSeen) {
		n.LastSeen = ts
	}
}
