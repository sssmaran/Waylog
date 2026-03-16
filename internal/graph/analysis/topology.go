package analysis

import (
	"math"
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

type TopologyNode struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Status      string  `json:"status"` // "healthy", "degraded", "failing"
	Invocations int     `json:"invocations"`
	Errors      int     `json:"errors"`
	ErrorRate   float64 `json:"error_rate"`
}

type TopologyEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Requests int    `json:"requests"`
	Failures int    `json:"failures"`
}

type TopologyResult struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
}

type CyNode struct {
	Data map[string]any `json:"data"`
}

type CyEdge struct {
	Data map[string]any `json:"data"`
}

type CytoscapeResult struct {
	Nodes []CyNode `json:"nodes"`
	Edges []CyEdge `json:"edges"`
}

type serviceStats struct {
	invocations int
	errors      int
}

type edgeKey struct {
	source, target string
}

type edgeStats struct {
	requests int
	failures int
}

// BuildTopology iterates span nodes in the graph, filters by the given time
// window, and computes per-service invocation/error stats plus caller→service
// edges. Returns never-nil slices.
func BuildTopology(graph *core.Graph, start, end time.Time) TopologyResult {
	services := map[string]*serviceStats{}
	edges := map[edgeKey]*edgeStats{}

	for _, n := range graph.Nodes {
		if n.Type != core.NodeSpan {
			continue
		}
		if n.LastSeen.Before(start) || n.LastSeen.After(end) {
			continue
		}

		svc, _ := n.Attr["service"].(string)
		if svc == "" {
			continue
		}

		ss := services[svc]
		if ss == nil {
			ss = &serviceStats{}
			services[svc] = ss
		}
		ss.invocations++

		if success, ok := n.Attr["success"].(bool); ok && !success {
			ss.errors++
		}

		caller, _ := n.Attr["caller_service"].(string)
		if caller != "" {
			// Ensure caller service appears in the map even with 0 invocations
			if services[caller] == nil {
				services[caller] = &serviceStats{}
			}

			ek := edgeKey{source: caller, target: svc}
			es := edges[ek]
			if es == nil {
				es = &edgeStats{}
				edges[ek] = es
			}
			es.requests++
			if success, ok := n.Attr["success"].(bool); ok && !success {
				es.failures++
			}
		}
	}

	nodes := make([]TopologyNode, 0, len(services))
	for id, ss := range services {
		var errRate float64
		if ss.invocations > 0 {
			errRate = float64(ss.errors) / float64(ss.invocations)
		}
		nodes = append(nodes, TopologyNode{
			ID:          id,
			Label:       id,
			Status:      statusFromErrorRate(errRate),
			Invocations: ss.invocations,
			Errors:      ss.errors,
			ErrorRate:   errRate,
		})
	}

	edgeList := make([]TopologyEdge, 0, len(edges))
	for ek, es := range edges {
		edgeList = append(edgeList, TopologyEdge{
			Source:   ek.source,
			Target:   ek.target,
			Requests: es.requests,
			Failures: es.failures,
		})
	}

	return TopologyResult{Nodes: nodes, Edges: edgeList}
}

// ToCytoscapeFormat converts a TopologyResult to Cytoscape JSON format
// compatible with the dashboard Graph tab expectations (error_rate as
// percentage, edges with "count" and "label":"calls").
func ToCytoscapeFormat(result TopologyResult) CytoscapeResult {
	nodes := make([]CyNode, 0, len(result.Nodes))
	for _, n := range result.Nodes {
		errPct := math.Round(n.ErrorRate*10000) / 100
		nodes = append(nodes, CyNode{
			Data: map[string]any{
				"id":          n.ID,
				"label":       n.Label,
				"type":        "service",
				"invocations": n.Invocations,
				"errors":      n.Errors,
				"error_rate":  errPct,
			},
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Data["id"].(string) < nodes[j].Data["id"].(string)
	})

	edges := make([]CyEdge, 0, len(result.Edges))
	for _, e := range result.Edges {
		edges = append(edges, CyEdge{
			Data: map[string]any{
				"source": e.Source,
				"target": e.Target,
				"label":  "calls",
				"count":  e.Requests,
			},
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		si := edges[i].Data["source"].(string)
		sj := edges[j].Data["source"].(string)
		if si != sj {
			return si < sj
		}
		return edges[i].Data["target"].(string) < edges[j].Data["target"].(string)
	})

	return CytoscapeResult{Nodes: nodes, Edges: edges}
}

func statusFromErrorRate(rate float64) string {
	if rate >= 0.5 {
		return "failing"
	}
	if rate >= 0.1 {
		return "degraded"
	}
	return "healthy"
}
