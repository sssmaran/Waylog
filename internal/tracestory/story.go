package tracestory

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// Hop represents a single service hop in a trace.
type Hop struct {
	SpanID     string    `json:"span_id"`
	Service    string    `json:"service"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	Success    bool      `json:"success"`
	ErrorCode  string    `json:"error_code,omitempty"`
	IsRoot     bool      `json:"is_root"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
}

// Story is the full trace narrative: an ordered chain of hops.
type Story struct {
	TraceID      string `json:"trace_id"`
	Chain        []Hop  `json:"chain"`
	Success      bool   `json:"success"`
	FirstFailHop *Hop   `json:"first_fail_hop,omitempty"`
	HopCount     int    `json:"hop_count"`
}

// Context provides user and request metadata for the trace.
type Context struct {
	RequestID    string   `json:"request_id,omitempty"`
	RequestEvent string   `json:"request_event,omitempty"`
	ErrorCodes   []string `json:"error_codes,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	UserTier     string   `json:"user_tier,omitempty"`
	UserRegion   string   `json:"user_region,omitempty"`
	Flow         string   `json:"flow,omitempty"`
	Flags        []string `json:"flags,omitempty"`
}

// Build constructs a Story and Context from a graph for the given traceID.
func Build(g *core.Graph, traceID string) (Story, Context, error) {
	if g == nil {
		return Story{}, Context{}, fmt.Errorf("graph is nil")
	}

	reqID := core.ID("request", traceID)
	reqNode, ok := g.Nodes[reqID]
	if !ok {
		return Story{}, Context{}, fmt.Errorf("trace %s not found", traceID)
	}

	// Find root spans: spans connected to the request that have no parent
	roots := rootSpanIDs(g, reqID)

	// Build parent → children map
	children := map[string][]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			// e.From = child, e.To = parent
			children[e.To] = append(children[e.To], e.From)
		}
	}
	sortSpanIDsByTime(g, roots)
	for parentID := range children {
		sortSpanIDsByTime(g, children[parentID])
	}

	// DFS from roots to build the hop chain in root-first order
	var chain []Hop
	visited := map[string]bool{}
	for _, root := range roots {
		dfsHops(g, root, children, visited, &chain)
	}

	// Build story
	story := Story{
		TraceID:  traceID,
		Chain:    chain,
		Success:  true,
		HopCount: len(chain),
	}
	for i := range chain {
		if !chain[i].Success {
			story.Success = false
			if story.FirstFailHop == nil {
				hop := chain[i] // copy
				story.FirstFailHop = &hop
			}
		}
	}

	// Build context
	ctx := buildContext(g, reqID, reqNode)

	return story, ctx, nil
}

// rootSpanIDs finds spans connected to the request that are NOT children of other spans.
func rootSpanIDs(g *core.Graph, reqID string) []string {
	// Collect all span IDs that are children (have a span_child_of edge FROM them)
	hasParent := map[string]bool{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			hasParent[e.From] = true
		}
	}

	// Find spans connected to this request that have no parent
	var roots []string
	seen := map[string]bool{}
	for _, e := range g.Edges {
		if e.Type != core.EdgeRequestHasSpan || e.From != reqID {
			continue
		}
		if seen[e.To] {
			continue
		}
		seen[e.To] = true
		if !hasParent[e.To] {
			roots = append(roots, e.To)
		}
	}
	return roots
}

// dfsHops traverses the span tree depth-first, building hops in root-first order.
func dfsHops(g *core.Graph, spanID string, children map[string][]string, visited map[string]bool, chain *[]Hop) {
	if visited[spanID] {
		return
	}
	visited[spanID] = true

	n, ok := g.Nodes[spanID]
	if !ok {
		return
	}

	*chain = append(*chain, hopFromNode(n))

	for _, childID := range children[spanID] {
		dfsHops(g, childID, children, visited, chain)
	}
}

// hopFromNode extracts a Hop from a span node's enriched attributes.
func hopFromNode(n core.Node) Hop {
	h := Hop{}
	if n.Attr == nil {
		return h
	}

	h.SpanID = stringAttr(n.Attr["span_id"])
	h.Service = stringAttr(n.Attr["service"])
	h.StatusCode, _ = intAttr(n.Attr["status_code"])
	h.LatencyMs, _ = int64Attr(n.Attr["latency_ms"])
	h.Success, _ = boolAttr(n.Attr["success"])
	h.ErrorCode = stringAttr(n.Attr["error_code"])
	h.IsRoot = stringAttr(n.Attr["parent_span_id"]) == ""
	h.Timestamp, _ = timeAttr(n.Attr["timestamp"])
	return h
}

// buildContext extracts user and request metadata for a trace.
func buildContext(g *core.Graph, reqID string, reqNode core.Node) Context {
	ctx := Context{RequestID: reqID}

	// Flow from request node
	if reqNode.Attr != nil {
		ctx.RequestEvent = stringAttr(reqNode.Attr["event_name"])
		ctx.Flow = stringAttr(reqNode.Attr["flow"])
		ctx.ErrorCodes = append(ctx.ErrorCodes, stringSliceAttr(reqNode.Attr["error_codes"])...)
		if code := stringAttr(reqNode.Attr["error_code"]); code != "" && len(ctx.ErrorCodes) == 0 {
			ctx.ErrorCodes = append(ctx.ErrorCodes, code)
		}
	}

	// Find user node via request_by edge
	for _, e := range g.Edges {
		if e.From == reqID && e.Type == core.EdgeRequestBy {
			if userNode, ok := g.Nodes[e.To]; ok && userNode.Attr != nil {
				ctx.UserTier = stringAttr(userNode.Attr["tier"])
				ctx.UserRegion = stringAttr(userNode.Attr["region"])
				// Extract user ID from the node ID (format: "user:<id>")
				ctx.UserID = e.To
				if uid := stringAttr(userNode.Attr["id"]); uid != "" {
					ctx.UserID = uid
				}
			}
			break
		}
	}

	// Find feature flags via used_flag edges
	for _, e := range g.Edges {
		if e.From == reqID && e.Type == core.EdgeUsedFlag {
			if flagNode, ok := g.Nodes[e.To]; ok && flagNode.Attr != nil {
				if name := stringAttr(flagNode.Attr["name"]); name != "" {
					ctx.Flags = append(ctx.Flags, name)
				}
			}
		}
	}

	return ctx
}

func sortSpanIDsByTime(g *core.Graph, spanIDs []string) {
	sort.Slice(spanIDs, func(i, j int) bool {
		leftTime := spanSortTime(g, spanIDs[i])
		rightTime := spanSortTime(g, spanIDs[j])
		if !leftTime.Equal(rightTime) {
			if leftTime.IsZero() {
				return false
			}
			if rightTime.IsZero() {
				return true
			}
			return leftTime.Before(rightTime)
		}
		return spanIDs[i] < spanIDs[j]
	})
}

func spanSortTime(g *core.Graph, spanID string) time.Time {
	n, ok := g.Nodes[spanID]
	if !ok {
		return time.Time{}
	}
	if ts, ok := timeAttr(n.Attr["timestamp"]); ok && !ts.IsZero() {
		return ts
	}
	if !n.FirstSeen.IsZero() {
		return n.FirstSeen
	}
	return n.LastSeen
}

func stringSliceAttr(v any) []string {
	switch values := v.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func stringAttr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func int64Attr(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case string:
		i, err := strconv.ParseInt(t, 10, 64)
		if err == nil {
			return i, true
		}
	}
	return 0, false
}

func intAttr(v any) (int, bool) {
	n, ok := int64Attr(v)
	return int(n), ok
}

func boolAttr(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		b, err := strconv.ParseBool(t)
		if err == nil {
			return b, true
		}
	}
	if n, ok := int64Attr(v); ok {
		return n != 0, true
	}
	return false, false
}

func timeAttr(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		ts, err := time.Parse(time.RFC3339Nano, t)
		if err == nil {
			return ts, true
		}
		ts, err = time.Parse(time.RFC3339, t)
		if err == nil {
			return ts, true
		}
	}
	if n, ok := int64Attr(v); ok {
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}
