package analysis

import (
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// ServiceImpact represents how many requests a given service was affected by.
type ServiceImpact struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
}

// BlastResult holds the blast-radius computation for a specific error code.
type BlastResult struct {
	AffectedRequests int             `json:"affected_requests"`
	AffectedUsers    int             `json:"affected_users"`
	Services         []ServiceImpact `json:"services"`
}

// ComputeBlastRadius computes the blast radius of a specific error code within
// the given time window. It works directly on a graph snapshot.
func ComputeBlastRadius(g *core.Graph, errorCode string, start, end time.Time) BlastResult {
	result := BlastResult{
		Services: []ServiceImpact{},
	}

	// Single pass over edges: find matched requests and build request→user index.
	matchedRequests := map[string]bool{}
	reqUsers := map[string][]string{}
	for _, e := range g.Edges {
		switch e.Type {
		case core.EdgeFailedWith:
			fromNode, ok := g.Nodes[e.From]
			if !ok || fromNode.Type != core.NodeRequest {
				continue
			}
			errNode, ok := g.Nodes[e.To]
			if !ok || errNode.Type != core.NodeError {
				continue
			}
			code, _ := errNode.Attr["code"].(string)
			if code != errorCode {
				continue
			}
			if fromNode.LastSeen.Before(start) || fromNode.LastSeen.After(end) {
				continue
			}
			matchedRequests[fromNode.ID] = true
		case core.EdgeRequestBy:
			reqUsers[e.From] = append(reqUsers[e.From], e.To)
		}
	}

	result.AffectedRequests = len(matchedRequests)

	// Count unique users and group by service.
	users := map[string]bool{}
	services := map[string]int{}

	for reqID := range matchedRequests {
		req := g.Nodes[reqID]
		svc := serviceFromRequest(req)
		if svc != "" {
			services[svc]++
		}
		for _, uid := range reqUsers[reqID] {
			users[uid] = true
		}
	}

	result.AffectedUsers = len(users)

	for svc, count := range services {
		result.Services = append(result.Services, ServiceImpact{
			Service: svc,
			Count:   count,
		})
	}

	return result
}

// serviceFromRequest extracts the canonical service name from a request node.
// Prefers root_service (set by root span merge), falls back to event_name prefix.
func serviceFromRequest(n core.Node) string {
	if n.Attr == nil {
		return ""
	}
	// root_service is the canonical owner, set by graph merge when the root span arrives.
	if svc, ok := n.Attr["root_service"].(string); ok && svc != "" {
		return svc
	}
	// Fall back to event_name prefix (e.g. "payment.error" -> "payment").
	if name, ok := n.Attr["event_name"].(string); ok {
		if idx := strings.IndexByte(name, '.'); idx > 0 {
			return name[:idx]
		}
	}
	return ""
}
