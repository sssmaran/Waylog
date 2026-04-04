package analysis

import (
	"math"
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
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

type edgeKey struct {
	source, target string
}

type edgeStats struct {
	requests int
	failures int
}

// BuildTopology aggregates service counters from the graph store and caller →
// service edge counts from the trace store. When traceStore is nil, it falls
// back to the legacy span graph for compatibility.
func BuildTopology(graphStore *graphstore.Store, traceStore *tracestore.Store, start, end time.Time) TopologyResult {
	services := map[string]graphstore.ServiceStats{}
	edges := map[edgeKey]*edgeStats{}

	if traceStore != nil {
		// Primary path: span-level data from trace store.
		traceStore.ForEachSpan(start, end, func(_ string, span tracestore.SpanRecord) {
			if span.Service != "" {
				stats := services[span.Service]
				stats.Invocations++
				if !span.Success {
					stats.Errors++
				}
				services[span.Service] = stats
			}
			if span.CallerService == "" || span.Service == "" {
				return
			}
			ek := edgeKey{source: span.CallerService, target: span.Service}
			es := edges[ek]
			if es == nil {
				es = &edgeStats{}
				edges[ek] = es
			}
			es.requests++
			if !span.Success {
				es.failures++
			}
			if _, ok := services[span.CallerService]; !ok {
				services[span.CallerService] = graphstore.ServiceStats{}
			}
		})
	} else if graphStore != nil {
		// Flattened graph path: service stats from RequestFacts, edges from
		// EdgeCalls in the graph (the builder still emits caller→service edges).
		graphStore.ForEachRequestFact(start, end, func(f graphstore.RequestFacts) {
			seen := map[string]bool{}
			for _, svc := range f.Services {
				if svc == "" || seen[svc] {
					continue
				}
				seen[svc] = true
				stats := services[svc]
				stats.Invocations++
				if len(f.Errors) > 0 {
					stats.Errors++
				}
				services[svc] = stats
			}
			if len(f.Services) == 0 && f.RootService != "" {
				stats := services[f.RootService]
				stats.Invocations++
				if len(f.Errors) > 0 {
					stats.Errors++
				}
				services[f.RootService] = stats
			}
		})

		// Derive caller→service edges from graph EdgeCalls edges.
		g := graphStore.Snapshot()
		for _, e := range g.Edges {
			if e.Type != core.EdgeCalls {
				continue
			}
			srcNode, srcOK := g.Nodes[e.From]
			tgtNode, tgtOK := g.Nodes[e.To]
			if !srcOK || !tgtOK {
				continue
			}
			if srcNode.Type != core.NodeService || tgtNode.Type != core.NodeService {
				continue
			}
			if srcNode.LastSeen.Before(start) && tgtNode.LastSeen.Before(start) {
				continue
			}
			srcName, _ := srcNode.Attr["name"].(string)
			tgtName, _ := tgtNode.Attr["name"].(string)
			if srcName == "" || tgtName == "" {
				continue
			}
			if _, ok := services[srcName]; !ok {
				services[srcName] = graphstore.ServiceStats{}
			}
			ek := edgeKey{source: srcName, target: tgtName}
			es := edges[ek]
			if es == nil {
				es = &edgeStats{}
				edges[ek] = es
			}
			es.requests++
		}
	}

	nodes := make([]TopologyNode, 0, len(services))
	for id, ss := range services {
		var errRate float64
		if ss.Invocations > 0 {
			errRate = float64(ss.Errors) / float64(ss.Invocations)
		}
		nodes = append(nodes, TopologyNode{
			ID:          id,
			Label:       id,
			Status:      statusFromErrorRate(errRate),
			Invocations: ss.Invocations,
			Errors:      ss.Errors,
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
