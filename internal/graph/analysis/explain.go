package analysis

import (
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// SpanSummary represents a span in the error propagation chain.
type SpanSummary struct {
	SpanID    string `json:"span_id"`
	Service   string `json:"service"`
	ErrorCode string `json:"error_code,omitempty"`
	LatencyMs any    `json:"latency_ms,omitempty"`
	Depth     int    `json:"depth"`
}

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

	SpanChain []SpanSummary
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

	// ---- span -> error (deepest root cause) ----
	type spanInfo struct {
		node    core.Node
		errNode *core.Node
	}
	spans := map[string]*spanInfo{}

	for _, e := range g.OutEdges[requestID] {
		if e.Type != core.EdgeRequestHasSpan {
			continue
		}
		spanNode, ok := g.Nodes[e.To]
		if !ok || spanNode.Type != core.NodeSpan {
			continue
		}
		si := &spanInfo{node: spanNode}
		for _, se := range g.OutEdges[spanNode.ID] {
			if se.Type == core.EdgeFailedWith {
				en := g.Nodes[se.To]
				si.errNode = &en
				break
			}
		}
		spans[spanNode.ID] = si
	}

	if len(spans) > 0 {
		// Build parent map: prefer EdgeSpanChildOf graph edges, fall back to parent_span_id attr.
		parentOf := map[string]string{} // spanID → parent spanID
		for spanID := range spans {
			// Check InEdges for EdgeSpanChildOf where this span is the child (e.From == spanID)
			for _, e := range g.OutEdges[spanID] {
				if e.Type == core.EdgeSpanChildOf {
					if _, ok := spans[e.To]; ok {
						parentOf[spanID] = e.To
					}
					break
				}
			}
			// Fall back to parent_span_id attr if no graph edge found
			if _, found := parentOf[spanID]; !found {
				if psid, ok := spans[spanID].node.Attr["parent_span_id"].(string); ok && psid != "" {
					// Reconstruct parent span node ID
					traceID, _ := spans[spanID].node.Attr["trace_id"].(string)
					if traceID != "" {
						parentNodeID := core.ID("span", traceID, psid)
						if _, ok := spans[parentNodeID]; ok {
							parentOf[spanID] = parentNodeID
						}
					}
				}
			}
		}

		// Compute depths (memoized, cycle-safe)
		depthCache := map[string]int{}
		visiting := map[string]bool{}
		var depth func(string) int
		depth = func(id string) int {
			if d, ok := depthCache[id]; ok {
				return d
			}
			if visiting[id] {
				// Cycle detected — treat as root
				depthCache[id] = 0
				return 0
			}
			visiting[id] = true
			pid, hasParent := parentOf[id]
			if !hasParent {
				depthCache[id] = 0
				delete(visiting, id)
				return 0
			}
			d := depth(pid) + 1
			depthCache[id] = d
			delete(visiting, id)
			return d
		}
		for id := range spans {
			depth(id)
		}

		// Find deepest error span; tiebreak by earlier timestamp
		var rootCauseID string
		rootCauseDepth := -1
		var rootCauseTime time.Time

		for id, si := range spans {
			if si.errNode == nil {
				continue
			}
			d := depthCache[id]
			ts := spanTimestamp(si.node)
			switch {
			case d > rootCauseDepth:
				rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
			case d == rootCauseDepth && !ts.IsZero() && (rootCauseTime.IsZero() || ts.Before(rootCauseTime)):
				rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
			case d == rootCauseDepth && ts.Equal(rootCauseTime) && id < rootCauseID:
				// Stable tiebreak: lexicographically smaller span ID wins
				rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
			}
		}

		if rootCauseID != "" {
			si := spans[rootCauseID]
			if si.errNode != nil && si.errNode.Attr != nil {
				ex.ErrorCode = si.errNode.Attr["code"]
				ex.ErrorMsg = si.errNode.Attr["message"]
			}
			ex.SpanID = rootCauseID
			if rootCauseDepth > 0 {
				ex.SpanDepth = "child"
			} else {
				ex.SpanDepth = "root"
			}
			ex.SpanService = spanServiceName(g, rootCauseID)

			// Build SpanChain: root-cause → root
			var chain []SpanSummary
			cur := rootCauseID
			visited := map[string]bool{}
			for cur != "" && !visited[cur] {
				visited[cur] = true
				si := spans[cur]
				ss := SpanSummary{
					SpanID:  cur,
					Service: spanServiceNameStr(g, cur),
					Depth:   depthCache[cur],
				}
				if si.node.Attr != nil {
					ss.LatencyMs = si.node.Attr["latency_ms"]
				}
				if si.errNode != nil && si.errNode.Attr != nil {
					ss.ErrorCode, _ = si.errNode.Attr["code"].(string)
				}
				chain = append(chain, ss)
				cur = parentOf[cur]
			}
			ex.SpanChain = chain

			// Populate user/flags/service and return
			populateUserFlagsService(g, requestID, &ex)
			return ex, nil
		}
	}

	// ---- request -> error (fallback) ----
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

	populateUserFlagsService(g, requestID, &ex)
	return ex, nil
}

func populateUserFlagsService(g *core.Graph, requestID string, ex *Explanation) {
	for _, e := range g.OutEdges[requestID] {
		switch e.Type {
		case core.EdgeRequestBy:
			u := g.Nodes[e.To]
			ex.UserID = u.ID
			if u.Attr != nil {
				ex.UserTier = u.Attr["tier"]
			}
		case core.EdgeUsedFlag:
			flagNode := g.Nodes[e.To]
			if flagNode.Attr != nil {
				if name, ok := flagNode.Attr["name"].(string); ok {
					ex.FeatureFlags = append(ex.FeatureFlags, name)
				}
			}
		case core.EdgeHandledBy:
			svcNode := g.Nodes[e.To]
			if svcNode.Attr != nil {
				ex.Service = svcNode.Attr["name"]
			}
		}
	}
}

func spanServiceName(g *core.Graph, spanID string) any {
	for _, e := range g.OutEdges[spanID] {
		if e.Type == core.EdgeSpanOnService {
			svc := g.Nodes[e.To]
			if svc.Attr != nil {
				return svc.Attr["name"]
			}
		}
	}
	return nil
}

func spanServiceNameStr(g *core.Graph, spanID string) string {
	v := spanServiceName(g, spanID)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func spanTimestamp(n core.Node) time.Time {
	if n.Attr == nil {
		return time.Time{}
	}
	switch v := n.Attr["timestamp"].(type) {
	case time.Time:
		return v
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
