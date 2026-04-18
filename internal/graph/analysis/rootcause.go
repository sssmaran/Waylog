package analysis

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

// RootCauseSpan selects the single root-cause span for a (usually failed)
// request using a deterministic tie-break. It is the canonical selector used
// by default user-facing rollups (see RollupWindow) and by failure
// explanations (ExplainRequest / ExplainRequestWithTrace).
//
// Tie-break order:
//  1. deepest failing span in the span tree wins
//  2. earliest timestamp breaks depth ties
//  3. lexicographic span ID breaks remaining ties
//
// Source precedence when resolving the cause:
//  1. trace store (when non-nil and a record exists for the request's trace)
//  2. graph span nodes reachable from the request
//  3. request-level failed_with edge (no span attribution)
//
// Return values:
//   - (spanID, errorCode, true) when a cause is found. spanID is empty when
//     the cause is a request-level error with no span attribution.
//   - ("", "", false) when the request has no failure information at all.
func RootCauseSpan(g *core.Graph, ts *tracestore.Store, requestID string) (string, string, bool) {
	if g == nil || requestID == "" {
		return "", "", false
	}
	req, ok := g.Nodes[requestID]
	if !ok {
		return "", "", false
	}

	if ts != nil {
		traceID := ""
		if req.Attr != nil {
			traceID, _ = req.Attr["trace_id"].(string)
		}
		if traceID != "" {
			if rec, found := ts.Get(traceID); found {
				if id, code, ok := rootCauseFromTraceRecord(rec); ok {
					return id, code, true
				}
			}
		}
	}

	if id, code, ok := rootCauseFromGraph(g, requestID); ok {
		return id, code, true
	}

	for _, e := range g.OutEdges[requestID] {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		errNode := g.Nodes[e.To]
		if errNode.Attr == nil {
			continue
		}
		if code, _ := errNode.Attr["code"].(string); code != "" {
			return "", code, true
		}
	}
	return "", "", false
}

type rootCauseCandidate struct {
	id    string
	depth int
	ts    time.Time
}

// pickRootCauseCandidate applies the deepest→earliest→lex tie-break and
// returns the winning id, or "" when candidates is empty.
func pickRootCauseCandidate(candidates []rootCauseCandidate) string {
	bestID := ""
	bestDepth := -1
	var bestTime time.Time
	for _, c := range candidates {
		switch {
		case c.depth > bestDepth:
			bestID, bestDepth, bestTime = c.id, c.depth, c.ts
		case c.depth == bestDepth && !c.ts.IsZero() && (bestTime.IsZero() || c.ts.Before(bestTime)):
			bestID, bestDepth, bestTime = c.id, c.depth, c.ts
		case c.depth == bestDepth && c.ts.Equal(bestTime) && c.id < bestID:
			bestID, bestDepth, bestTime = c.id, c.depth, c.ts
		}
	}
	return bestID
}

func rootCauseFromTraceRecord(rec *tracestore.TraceRecord) (string, string, bool) {
	if rec == nil || len(rec.Spans) == 0 {
		return "", "", false
	}
	spans := make(map[string]tracestore.SpanRecord, len(rec.Spans))
	parentOf := map[string]string{}
	for _, span := range rec.Spans {
		if span.SpanID == "" {
			continue
		}
		spans[span.SpanID] = span
		if span.ParentSpanID != "" {
			parentOf[span.SpanID] = span.ParentSpanID
		}
	}
	if len(spans) == 0 {
		return "", "", false
	}

	depths := computeSpanDepths(spans, parentOf)

	candidates := make([]rootCauseCandidate, 0, len(spans))
	for id, span := range spans {
		if span.Success || span.ErrorCode == "" {
			continue
		}
		candidates = append(candidates, rootCauseCandidate{id: id, depth: depths[id], ts: span.Timestamp})
	}
	rootID := pickRootCauseCandidate(candidates)
	if rootID == "" {
		return "", "", false
	}
	return rootID, spans[rootID].ErrorCode, true
}

func rootCauseFromGraph(g *core.Graph, requestID string) (string, string, bool) {
	type info struct {
		node    core.Node
		errCode string
	}
	spans := map[string]*info{}
	for _, e := range g.OutEdges[requestID] {
		if e.Type != core.EdgeRequestHasSpan {
			continue
		}
		sn, ok := g.Nodes[e.To]
		if !ok || sn.Type != core.NodeSpan {
			continue
		}
		si := &info{node: sn}
		for _, se := range g.OutEdges[sn.ID] {
			if se.Type != core.EdgeFailedWith {
				continue
			}
			en, ok := g.Nodes[se.To]
			if !ok || en.Attr == nil {
				break
			}
			if code, _ := en.Attr["code"].(string); code != "" {
				si.errCode = code
			}
			break
		}
		spans[sn.ID] = si
	}
	if len(spans) == 0 {
		return "", "", false
	}

	parentOf := map[string]string{}
	for spanID, si := range spans {
		for _, e := range g.OutEdges[spanID] {
			if e.Type == core.EdgeSpanChildOf {
				if _, ok := spans[e.To]; ok {
					parentOf[spanID] = e.To
				}
				break
			}
		}
		if _, found := parentOf[spanID]; !found {
			if psid, ok := si.node.Attr["parent_span_id"].(string); ok && psid != "" {
				traceID, _ := si.node.Attr["trace_id"].(string)
				if traceID != "" {
					parentNodeID := core.ID("span", traceID, psid)
					if _, ok := spans[parentNodeID]; ok {
						parentOf[spanID] = parentNodeID
					}
				}
			}
		}
	}

	depths := computeSpanDepths(spans, parentOf)

	candidates := make([]rootCauseCandidate, 0, len(spans))
	for id, si := range spans {
		if si.errCode == "" {
			continue
		}
		candidates = append(candidates, rootCauseCandidate{id: id, depth: depths[id], ts: spanTimestamp(si.node)})
	}
	rootID := pickRootCauseCandidate(candidates)
	if rootID == "" {
		return "", "", false
	}
	return rootID, spans[rootID].errCode, true
}

// computeSpanDepths walks the parent chain for each span and returns the depth
// from root. Cycles and orphan parents (outside the member set) yield depth 0.
func computeSpanDepths[T any](spans map[string]T, parentOf map[string]string) map[string]int {
	depth := map[string]int{}
	visiting := map[string]bool{}
	var walk func(string) int
	walk = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		if visiting[id] {
			depth[id] = 0
			return 0
		}
		visiting[id] = true
		pid, has := parentOf[id]
		if !has || pid == "" {
			depth[id] = 0
			delete(visiting, id)
			return 0
		}
		if _, ok := spans[pid]; !ok {
			depth[id] = 0
			delete(visiting, id)
			return 0
		}
		d := walk(pid) + 1
		depth[id] = d
		delete(visiting, id)
		return d
	}
	for id := range spans {
		walk(id)
	}
	return depth
}
