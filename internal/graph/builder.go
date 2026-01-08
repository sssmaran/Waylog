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
	// Service node
	// --------------------
	svcID := ID("service", ev.System.Service)
	g.AddNode(Node{
		ID:   svcID,
		Type: NodeService,
		Attr: map[string]any{
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
