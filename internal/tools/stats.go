package tools

import (
	"context"
	"encoding/json"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

type graphStatsOutput struct {
	Nodes        int `json:"nodes"`
	Edges        int `json:"edges"`
	Requests     int `json:"requests"`
	Users        int `json:"users"`
	Services     int `json:"services"`
	FeatureFlags int `json:"feature_flags"`
	Failures     int `json:"failures"`
}

func handleGraphStats(ctx context.Context, store Store, _ json.RawMessage) (any, error) {
	_ = ctx
	g := store.Snapshot()
	out := graphStatsOutput{
		Nodes: len(g.Nodes),
		Edges: len(g.Edges),
	}

	for _, n := range g.Nodes {
		switch n.Type {
		case core.NodeRequest:
			out.Requests++
		case core.NodeUser:
			out.Users++
		case core.NodeService:
			out.Services++
		case core.NodeFlag:
			out.FeatureFlags++
		case core.NodeError:
			out.Failures++
		}
	}

	return out, nil
}
