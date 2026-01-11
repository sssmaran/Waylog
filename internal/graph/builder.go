package graph

import (
	"github.com/sssmaran/WaylogCLI/internal/event"
)

type Builder struct{}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Build(ev event.WideEvent) *Graph {
	g := New()

	// --------------------
	// Request node
	// --------------------
	reqID := ID("request", ev.Request.TraceID)
	g.AddNode(Node{
		ID:   reqID,
		Type: NodeRequest,
		Attr: map[string]any{
			"event_name":  ev.EventName,
			"flow":        ev.Request.Flow,
			"latency_ms":  ev.Metrics.LatencyMs,
			"success":     ev.Outcome.Success,
			"status_code": ev.Outcome.StatusCode,
			"timestamp":   ev.Timestamp,
		},
	})

	// --------------------
	// User node
	// --------------------
	userID := ID("user", ev.User.ID)
	g.AddNode(Node{
		ID:   userID,
		Type: NodeUser,
		Attr: map[string]any{
			"tier":   ev.User.Tier,
			"region": ev.User.Region,
			"vip":    ev.User.VIP,
		},
	})
	g.AddEdge(Edge{
		From: reqID,
		To:   userID,
		Type: EdgeRequestBy,
	})

	// --------------------
	// Service node (FIXED)
	// --------------------
	svcID := ID(
		"service",
		ev.System.Service,
		ev.System.Env,
	)
	g.AddNode(Node{
		ID:   svcID,
		Type: NodeService,
		Attr: map[string]any{
			"name":          ev.System.Service,
			"env":           ev.System.Env,
			"version":       ev.System.Version,
			"deployment_id": ev.System.DeploymentID,
		},
	})
	g.AddEdge(Edge{
		From: reqID,
		To:   svcID,
		Type: EdgeHandledBy,
	})

	// --------------------
	// Feature flag nodes
	// --------------------
	for _, flag := range ev.Request.FeatureFlags {
		flagID := ID("feature_flag", flag)
		g.AddNode(Node{
			ID:   flagID,
			Type: NodeFlag,
			Attr: map[string]any{
				"name": flag,
			},
		})
		g.AddEdge(Edge{
			From: reqID,
			To:   flagID,
			Type: EdgeUsedFlag,
		})
	}

	// --------------------
// Service-to-service call edge
// --------------------
if ev.System.CallerService != "" {
	callerID := ID("service", ev.System.CallerService)

	// Ensure caller service node exists
	g.AddNode(Node{
		ID:   callerID,
		Type: NodeService,
		Attr: map[string]any{
			"env": ev.System.Env,
		},
	})

	// caller_service -> calls -> service
	g.AddEdge(Edge{
		From: callerID,
		To:   svcID,
		Type: EdgeCalls,
	})
}
	// --------------------
// Downstream service dependency (optional)
// --------------------
if ev.System.DownstreamService != "" {
	downID := ID("service", ev.System.DownstreamService)

	g.AddNode(Node{
		ID:   downID,
		Type: NodeService,
		Attr: map[string]any{
			"env": ev.System.Env,
		},
	})

	g.AddEdge(Edge{
		From: svcID,
		To:   downID,
		Type: EdgeCalls,
	})
}

	// --------------------
	// Error node (only if present)
	// --------------------
	if ev.Error != nil {
		errID := ID("error", ev.Error.Code)
		g.AddNode(Node{
			ID:   errID,
			Type: NodeError,
			Attr: map[string]any{
				"code":    ev.Error.Code,
				"message": ev.Error.Message,
			},
		})
		g.AddEdge(Edge{
			From: reqID,
			To:   errID,
			Type: EdgeFailedWith,
		})
	}

	return g
}
