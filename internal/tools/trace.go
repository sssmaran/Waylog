package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

type traceGraphInput struct {
	TraceID string `json:"trace_id"`
}

type traceSpan struct {
	SpanID   string      `json:"span_id,omitempty"`
	Service  any         `json:"service,omitempty"`
	Children []traceSpan `json:"children,omitempty"`
}

type traceGraphOutput struct {
	SchemaVersion string      `json:"schema_version"`
	TraceID       string      `json:"trace_id"`
	Roots         []traceSpan `json:"roots"`
}

func handleTraceGraph(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceGraphInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.TraceID == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "trace_id required"}
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)
	var roots []traceSpan

	if ts := traceStoreFrom(store); ts != nil {
		if rec, ok := ts.Get(input.TraceID); ok {
			roots = traceTreeToSpans(tracestore.BuildTree(rec.Spans))
		}
	}
	if len(roots) == 0 {
		for _, spanID := range rootSpanIDsFromGraph(g, reqID) {
			roots = append(roots, buildTraceSpanFromGraph(g, spanID, map[string]bool{}))
		}
	}

	return traceGraphOutput{
		SchemaVersion: "1.0",
		TraceID:       input.TraceID,
		Roots:         roots,
	}, nil
}

func buildTraceSpanFromGraph(g *core.Graph, spanID string, visited map[string]bool) traceSpan {
	if visited[spanID] {
		return traceSpan{}
	}
	visited[spanID] = true

	n, ok := g.Nodes[spanID]
	if !ok {
		return traceSpan{}
	}

	out := traceSpan{
		Service: n.Attr["service"],
	}
	if span, ok := n.Attr["span_id"]; ok && span != nil {
		out.SpanID = fmt.Sprintf("%v", span)
	}

	for _, e := range g.InEdges[spanID] {
		if e.Type == core.EdgeSpanChildOf {
			out.Children = append(out.Children, buildTraceSpanFromGraph(g, e.From, visited))
		}
	}

	return out
}

type traceSummaryInput struct {
	TraceID string `json:"trace_id"`
}

type traceSummaryOutput struct {
	SchemaVersion   string     `json:"schema_version"`
	TraceID         string     `json:"trace_id"`
	RequestID       string     `json:"request_id"`
	EventName       string     `json:"event_name,omitempty"`
	Flow            string     `json:"flow,omitempty"`
	LatencyMs       any        `json:"latency_ms,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ErrorPath       string     `json:"error_path,omitempty"`
	ErrorReason     string     `json:"error_reason,omitempty"`
	RetryOf         int        `json:"retry_of,omitempty"`
	RetryPreviousID string     `json:"retry_previous_attempt_id,omitempty"`
	ParentRequestID string     `json:"parent_request_id,omitempty"`
	RootSpanIDs     []string   `json:"root_span_ids,omitempty"`
	Paths           [][]string `json:"paths,omitempty"`
}

func handleTraceSummary(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceSummaryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.TraceID == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "trace_id required"}
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)

	out := traceSummaryOutput{
		SchemaVersion: "1.0",
		TraceID:       input.TraceID,
		RequestID:     reqID,
	}

	if req, ok := g.Nodes[reqID]; ok {
		if req.Attr != nil {
			if name, ok := req.Attr["event_name"].(string); ok {
				out.EventName = name
			}
			if flow, ok := req.Attr["flow"].(string); ok {
				out.Flow = flow
			}
			out.LatencyMs = req.Attr["latency_ms"]
			if code, ok := req.Attr["error_code"].(string); ok {
				out.ErrorCode = code
			}
			if path, ok := req.Attr["error_path"].(string); ok {
				out.ErrorPath = path
			}
			if reason, ok := req.Attr["error_reason"].(string); ok {
				out.ErrorReason = reason
			}
			if parent, ok := req.Attr["parent_request_id"].(string); ok {
				out.ParentRequestID = parent
			}
			if prev, ok := req.Attr["retry_previous_attempt_id"].(string); ok {
				out.RetryPreviousID = prev
			}
			switch v := req.Attr["retry_of"].(type) {
			case int:
				out.RetryOf = v
			case float64:
				out.RetryOf = int(v)
			}
		}
	}

	if ts := traceStoreFrom(store); ts != nil {
		if rec, ok := ts.Get(input.TraceID); ok {
			out.RequestID = rec.RequestID
			roots := tracestore.BuildTree(rec.Spans)
			out.RootSpanIDs = traceRootIDs(roots)
			out.Paths = traceTreePaths(roots)
		}
	}
	if len(out.RootSpanIDs) == 0 {
		rootSpans := rootSpanIDsFromGraph(g, reqID)
		out.RootSpanIDs = rootSpans
		out.Paths = spanPathsForRootsFromGraph(g, rootSpans)
	}

	if len(out.Paths) == 0 {
		if chain := serviceChainForRequest(g, reqID); len(chain) > 0 {
			out.Paths = [][]string{chain}
		}
	}

	return out, nil
}

func traceTreeToSpans(nodes []*tracestore.TreeNode) []traceSpan {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]traceSpan, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, traceSpan{
			SpanID:   node.Span.SpanID,
			Service:  node.Span.Service,
			Children: traceTreeToSpans(node.Children),
		})
	}
	return out
}

func traceRootIDs(nodes []*tracestore.TreeNode) []string {
	if len(nodes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Span.SpanID != "" {
			ids = append(ids, node.Span.SpanID)
		}
	}
	return ids
}

func traceTreePaths(nodes []*tracestore.TreeNode) [][]string {
	var paths [][]string
	var walk func(node *tracestore.TreeNode, prefix []string)
	walk = func(node *tracestore.TreeNode, prefix []string) {
		if node == nil {
			return
		}
		service := node.Span.Service
		if service == "" {
			service = node.Span.SpanID
		}
		next := append(prefix, service)
		if len(node.Children) == 0 {
			paths = append(paths, next)
			return
		}
		for _, child := range node.Children {
			walk(child, next)
		}
	}
	for _, root := range nodes {
		walk(root, nil)
	}
	return paths
}

func rootSpanIDsFromGraph(g *core.Graph, reqID string) []string {
	hasParent := map[string]bool{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			hasParent[e.From] = true
		}
	}
	var roots []string
	seen := map[string]bool{}
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeRequestHasSpan {
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

func spanPathsForRootsFromGraph(g *core.Graph, roots []string) [][]string {
	if len(roots) == 0 {
		return nil
	}
	children := map[string][]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			children[e.To] = append(children[e.To], e.From)
		}
	}

	var paths [][]string
	for _, root := range roots {
		dfsSpanPathsFromGraph(g, root, children, nil, &paths)
	}
	return paths
}

func dfsSpanPathsFromGraph(g *core.Graph, spanID string, children map[string][]string, prefix []string, out *[][]string) {
	n, ok := g.Nodes[spanID]
	if !ok {
		return
	}
	service := ""
	if n.Attr != nil {
		if s, ok := n.Attr["service"].(string); ok {
			service = s
		}
	}
	if service == "" {
		service = spanID
	}
	path := append(prefix, service)
	kids := children[spanID]
	if len(kids) == 0 {
		*out = append(*out, path)
		return
	}
	for _, child := range kids {
		dfsSpanPathsFromGraph(g, child, children, path, out)
	}
}
