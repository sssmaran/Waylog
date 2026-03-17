package window

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// FilterByWindow returns a graph containing ONLY
// requests active in [start, end] and their 1-hop context.
func FilterByWindow(g *core.Graph, start, end time.Time) *core.Graph {
	if g == nil {
		return core.New()
	}

	out := core.New()
	keep := map[string]bool{}

	// 1. Keep request nodes active in window
	for id, n := range g.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if n.LastSeen.IsZero() {
			continue
		}
		if !n.LastSeen.Before(start) && !n.LastSeen.After(end) {
			keep[id] = true
		}
	}

	// 2. Pull in 1-hop neighbors via adjacency indexes
	for id := range keep {
		for _, e := range g.OutEdges[id] {
			keep[e.To] = true
		}
		for _, e := range g.InEdges[id] {
			keep[e.From] = true
		}
	}

	// 3. Copy nodes
	for id := range keep {
		if n, ok := g.Nodes[id]; ok {
			out.Nodes[id] = n
		}
	}

	// 4. Copy edges and rebuild indexes
	for _, e := range g.Edges {
		if keep[e.From] && keep[e.To] {
			out.AddEdge(e)
		}
	}

	return out
}
