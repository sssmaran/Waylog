package build

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type Builder struct{}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Build(ev event.WideEvent) *core.Graph {
	g := core.New()
	errID := ""

	// --------------------
	// Request node
	// --------------------
	reqID := core.ID("request", ev.Request.TraceID)
	req := core.Node{
		ID:   reqID,
		Type: core.NodeRequest,
		Attr: map[string]any{
			"event_name":  ev.EventName,
			"trace_id":    ev.Request.TraceID,
			"flow":        ev.Request.Flow,
			"latency_ms":  ev.Metrics.LatencyMs,
			"success":     ev.Outcome.Success,
			"status_code": ev.Outcome.StatusCode,
		},
	}
	touch(&req, ev.Timestamp)
	g.AddNode(req)

	// --------------------
	// User node
	// --------------------
	userID := core.ID("user", ev.User.ID)
	user := core.Node{
		ID:   userID,
		Type: core.NodeUser,
		Attr: map[string]any{
			"tier":   ev.User.Tier,
			"region": ev.User.Region,
			"vip":    ev.User.VIP,
		},
	}
	touch(&user, ev.Timestamp)
	g.AddNode(user)

	g.AddEdge(core.Edge{
		From: reqID,
		To:   userID,
		Type: core.EdgeRequestBy,
	})

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
	// Span node
	// --------------------
	var spanNodeID string

	if ev.Request.SpanID != "" {
		spanNodeID = core.ID("span", ev.Request.TraceID, ev.Request.SpanID)

		span := core.Node{
			ID:   spanNodeID,
			Type: core.NodeSpan,
			Attr: map[string]any{
				"trace_id":       ev.Request.TraceID,
				"span_id":        ev.Request.SpanID,
				"parent_span_id": ev.Request.ParentSpanID,
				"service":        ev.System.Service,
			},
		}
		touch(&span, ev.Timestamp)
		g.AddNode(span)

		// request → span
		g.AddEdge(core.Edge{
			From: reqID,
			To:   spanNodeID,
			Type: core.EdgeRequestHasSpan,
		})

		// span → service
		g.AddEdge(core.Edge{
			From: spanNodeID,
			To:   svcID,
			Type: core.EdgeSpanOnService,
		})

		// span → parent span (if exists)
		if ev.Request.ParentSpanID != "" {
			parentID := core.ID("span", ev.Request.TraceID, ev.Request.ParentSpanID)

			parent := core.Node{
				ID:   parentID,
				Type: core.NodeSpan,
				Attr: map[string]any{
					"trace_id": ev.Request.TraceID,
					"span_id":  ev.Request.ParentSpanID,
				},
			}
			touch(&parent, ev.Timestamp)
			g.AddNode(parent)

			g.AddEdge(core.Edge{
				From: spanNodeID,
				To:   parentID,
				Type: core.EdgeSpanChildOf,
			})
		}
	}
	// --------------------
	// Feature flag nodes
	// --------------------
	for _, flag := range ev.Request.FeatureFlags {
		flagID := core.ID("feature_flag", flag)
		flagNode := core.Node{
			ID:   flagID,
			Type: core.NodeFlag,
			Attr: map[string]any{
				"name": flag,
			},
		}
		touch(&flagNode, ev.Timestamp)
		g.AddNode(flagNode)

		g.AddEdge(core.Edge{
			From: reqID,
			To:   flagID,
			Type: core.EdgeUsedFlag,
		})
	}

	// --------------------
	// Service-to-service call edge
	// --------------------
	if ev.System.CallerService != "" {
		callerID := core.ID("service", ev.System.CallerService)

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
		downID := core.ID("service", ev.System.DownstreamService)

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
			},
		}
		touch(&errNode, ev.Timestamp)
		g.AddNode(errNode)

		g.AddEdge(core.Edge{
			From: reqID,
			To:   errID,
			Type: core.EdgeFailedWith,
		})

		// span → error (only when both exist)
		if spanNodeID != "" {
			g.AddEdge(core.Edge{
				From: spanNodeID,
				To:   errID,
				Type: core.EdgeFailedWith,
			})
		}
	}

	return g
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
