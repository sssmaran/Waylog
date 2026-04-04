package analysis

import (
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
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

// ExplainRequest reconstructs failure context for a request node using the
// legacy graph path. Prefer ExplainRequestWithTrace when a trace store is
// available.
func ExplainRequest(g *core.Graph, requestID string) (Explanation, error) {
	return ExplainRequestWithTrace(g, nil, requestID)
}

// ExplainRequestWithTrace reconstructs failure context from the graph plus an
// optional trace store. When trace data is available, the span chain and root
// cause are sourced from the flat trace record rather than graph span nodes.
func ExplainRequestWithTrace(g *core.Graph, traceStore *tracestore.Store, requestID string) (Explanation, error) {
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

	if traceStore != nil {
		traceID := ""
		if req.Attr != nil {
			traceID, _ = req.Attr["trace_id"].(string)
		}
		if rec, ok := traceStore.Get(traceID); ok {
			if traced, ok := explainFromTraceRecord(g, req, requestID, rec); ok {
				return traced, nil
			}
		}
	}

	// ---- graph span/error fallback ----
	if ex, ok := explainFromGraph(g, requestID); ok {
		return ex, nil
	}

	populateUserFlagsService(g, requestID, &ex)
	return ex, nil
}

func explainFromTraceRecord(g *core.Graph, req core.Node, requestID string, rec *tracestore.TraceRecord) (Explanation, bool) {
	if rec == nil || len(rec.Spans) == 0 {
		return Explanation{}, false
	}
	ex := Explanation{RequestID: requestID}
	if req.Attr != nil {
		ex.LatencyMs = req.Attr["latency_ms"]
		ex.Flow = req.Attr["flow"]
	}

	spans := map[string]tracestore.SpanRecord{}
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
		return Explanation{}, false
	}

	depthCache := map[string]int{}
	visiting := map[string]bool{}
	var depth func(string) int
	depth = func(id string) int {
		if d, ok := depthCache[id]; ok {
			return d
		}
		if visiting[id] {
			depthCache[id] = 0
			return 0
		}
		visiting[id] = true
		pid, hasParent := parentOf[id]
		if !hasParent || pid == "" {
			depthCache[id] = 0
			delete(visiting, id)
			return 0
		}
		if _, ok := spans[pid]; !ok {
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

	var rootCauseID string
	rootCauseDepth := -1
	var rootCauseTime time.Time
	for id, span := range spans {
		if span.Success || span.ErrorCode == "" {
			continue
		}
		d := depthCache[id]
		ts := span.Timestamp
		switch {
		case d > rootCauseDepth:
			rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
		case d == rootCauseDepth && !ts.IsZero() && (rootCauseTime.IsZero() || ts.Before(rootCauseTime)):
			rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
		case d == rootCauseDepth && ts.Equal(rootCauseTime) && id < rootCauseID:
			rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
		}
	}
	if rootCauseID == "" {
		for _, e := range g.OutEdges[requestID] {
			if e.Type == core.EdgeFailedWith {
				errNode := g.Nodes[e.To]
				if errNode.Attr != nil {
					ex.ErrorCode = errNode.Attr["code"]
					ex.ErrorMsg = errNode.Attr["message"]
				}
				populateUserFlagsService(g, requestID, &ex)
				return ex, true
			}
		}
		return Explanation{}, false
	}

	rc := spans[rootCauseID]
	ex.ErrorCode = rc.ErrorCode
	ex.ErrorMsg = rc.ErrorMessage
	ex.SpanID = rootCauseID
	if rootCauseDepth > 0 {
		ex.SpanDepth = "child"
	} else {
		ex.SpanDepth = "root"
	}
	ex.SpanService = rc.Service
	if req.Attr != nil {
		ex.Service = stringAttr(req.Attr["service"])
	}

	cur := rootCauseID
	visited := map[string]bool{}
	for cur != "" && !visited[cur] {
		visited[cur] = true
		span := spans[cur]
		ss := SpanSummary{
			SpanID:    cur,
			Service:   span.Service,
			ErrorCode: span.ErrorCode,
			LatencyMs: span.LatencyMs,
			Depth:     depthCache[cur],
		}
		ex.SpanChain = append(ex.SpanChain, ss)
		cur = parentOf[cur]
	}
	populateUserFlagsService(g, requestID, &ex)
	return ex, true
}

func explainFromGraph(g *core.Graph, requestID string) (Explanation, bool) {
	req, ok := g.Nodes[requestID]
	if !ok {
		return Explanation{}, false
	}

	ex := Explanation{
		RequestID: requestID,
	}
	if req.Attr != nil {
		ex.LatencyMs = req.Attr["latency_ms"]
		ex.Flow = req.Attr["flow"]
	}

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

	if len(spans) == 0 {
		for _, e := range g.OutEdges[requestID] {
			if e.Type == core.EdgeFailedWith {
				errNode := g.Nodes[e.To]
				if errNode.Attr != nil {
					ex.ErrorCode = errNode.Attr["code"]
					ex.ErrorMsg = errNode.Attr["message"]
				}
				populateUserFlagsService(g, requestID, &ex)
				return ex, true
			}
		}
		return Explanation{}, false
	}

	parentOf := map[string]string{}
	for spanID := range spans {
		for _, e := range g.OutEdges[spanID] {
			if e.Type == core.EdgeSpanChildOf {
				if _, ok := spans[e.To]; ok {
					parentOf[spanID] = e.To
				}
				break
			}
		}
		if _, found := parentOf[spanID]; !found {
			if psid, ok := spans[spanID].node.Attr["parent_span_id"].(string); ok && psid != "" {
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

	depthCache := map[string]int{}
	visiting := map[string]bool{}
	var depth func(string) int
	depth = func(id string) int {
		if d, ok := depthCache[id]; ok {
			return d
		}
		if visiting[id] {
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
			rootCauseID, rootCauseDepth, rootCauseTime = id, d, ts
		}
	}

	if rootCauseID == "" {
		for _, e := range g.OutEdges[requestID] {
			if e.Type == core.EdgeFailedWith {
				errNode := g.Nodes[e.To]
				if errNode.Attr != nil {
					ex.ErrorCode = errNode.Attr["code"]
					ex.ErrorMsg = errNode.Attr["message"]
				}
				populateUserFlagsService(g, requestID, &ex)
				return ex, true
			}
		}
		return Explanation{}, false
	}
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
	populateUserFlagsService(g, requestID, &ex)
	return ex, true
}

func stringAttr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func populateUserFlagsService(g *core.Graph, requestID string, ex *Explanation) {
	req, _ := g.Nodes[requestID]
	if req.Attr != nil {
		if uid, ok := req.Attr["user_id"].(string); ok && uid != "" {
			ex.UserID = uid
		}
		if tier, ok := req.Attr["user_tier"].(string); ok && tier != "" {
			ex.UserTier = tier
		}
		if flags := requestFeatureFlagsFromNode(req); len(flags) > 0 {
			ex.FeatureFlags = append(ex.FeatureFlags, flags...)
		}
		if svc, ok := req.Attr["root_service"].(string); ok && svc != "" {
			ex.Service = svc
		} else if svc, ok := req.Attr["service"].(string); ok && svc != "" {
			ex.Service = svc
		}
	}
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

func requestFeatureFlagsFromNode(req core.Node) []string {
	if req.Attr == nil {
		return nil
	}
	if flags, ok := req.Attr["feature_flags"].([]string); ok {
		return append([]string(nil), flags...)
	}
	if flags, ok := req.Attr["feature_flags"].([]any); ok {
		out := make([]string, 0, len(flags))
		for _, item := range flags {
			if name, ok := item.(string); ok && name != "" {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
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
