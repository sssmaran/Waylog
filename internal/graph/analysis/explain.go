package analysis

import (
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// Explanation is a deterministic, structured view of why a request failed.
// It is a projection of graph structure — not inference.
type Explanation struct {
	RequestID string

	LatencyMs any
	Flow      any

	UserID   string
	UserTier any

	FeatureFlags []string

	//span-aware attributes
	SpanID      string
	SpanService any
	SpanDepth   string // "root" | "child"

	Service any

	ErrorCode any
	ErrorMsg  any
}

// ExplainRequest reconstructs failure context for a request node.
// It ONLY reports what exists in the graph.
func ExplainRequest(g *core.Graph, requestID string) (Explanation, error) {
	req, ok := g.Nodes[requestID]
	if !ok {
		return Explanation{}, fmt.Errorf("request node not found: %s", requestID)
	}

	ex := Explanation{
		RequestID: requestID,
	}

	// ---- request attributes ----
	if req.Attr != nil {
		ex.LatencyMs = req.Attr["latency_ms"]
		ex.Flow = req.Attr["flow"]
	}

	// ---- span -> error (preferred) — check all spans via outEdges ----
	for _, e := range g.OutEdges[requestID] {
		if e.Type != core.EdgeRequestHasSpan {
			continue
		}
		spanNode, ok := g.Nodes[e.To]
		if !ok || spanNode.Type != core.NodeSpan {
			continue
		}
		for _, se := range g.OutEdges[spanNode.ID] {
			if se.Type != core.EdgeFailedWith {
				continue
			}
			errNode := g.Nodes[se.To]
			if errNode.Attr != nil {
				ex.ErrorCode = errNode.Attr["code"]
				ex.ErrorMsg = errNode.Attr["message"]
			}
			ex.SpanID = spanNode.ID
			if parent := spanNode.Attr["parent_span_id"]; parent != nil && parent != "" {
				ex.SpanDepth = "child"
			} else {
				ex.SpanDepth = "root"
			}
			for _, svcE := range g.OutEdges[spanNode.ID] {
				if svcE.Type == core.EdgeSpanOnService {
					svc := g.Nodes[svcE.To]
					if svc.Attr != nil {
						ex.SpanService = svc.Attr["name"]
					}
					break
				}
			}
			return ex, nil
		}
	}

	// ---- request -> error ----
	for _, e := range g.OutEdges[requestID] {
		if e.Type == core.EdgeFailedWith {
			errNode := g.Nodes[e.To]
			if errNode.Attr != nil {
				ex.ErrorCode = errNode.Attr["code"]
				ex.ErrorMsg = errNode.Attr["message"]
			}
			break
		}
	}

	// ---- request -> user ----
	for _, e := range g.OutEdges[requestID] {
		if e.Type == core.EdgeRequestBy {
			u := g.Nodes[e.To]
			ex.UserID = u.ID
			if u.Attr != nil {
				ex.UserTier = u.Attr["tier"]
			}
			break
		}
	}

	// ---- request -> feature flags ----
	for _, e := range g.OutEdges[requestID] {
		if e.Type == core.EdgeUsedFlag {
			flagNode := g.Nodes[e.To]
			if flagNode.Attr != nil {
				if name, ok := flagNode.Attr["name"].(string); ok {
					ex.FeatureFlags = append(ex.FeatureFlags, name)
				}
			}
		}
	}

	// ---- request -> service ----
	for _, e := range g.OutEdges[requestID] {
		if e.Type == core.EdgeHandledBy {
			svcNode := g.Nodes[e.To]
			if svcNode.Attr != nil {
				ex.Service = svcNode.Attr["name"]
			}
			break
		}
	}

	return ex, nil
}
