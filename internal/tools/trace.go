package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
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
	TraceID string      `json:"trace_id"`
	Roots   []traceSpan `json:"roots"`
}

func handleTraceGraph(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceGraphInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.TraceID == "" {
		return nil, fmt.Errorf("trace_id required")
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)
	var roots []traceSpan

	for _, spanID := range rootSpanIDsForTrace(g, reqID) {
		roots = append(roots, buildTraceSpan(g, spanID, map[string]bool{}))
	}

	return traceGraphOutput{
		TraceID: input.TraceID,
		Roots:   roots,
	}, nil
}

func buildTraceSpan(g *core.Graph, spanID string, visited map[string]bool) traceSpan {
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
			out.Children = append(out.Children, buildTraceSpan(g, e.From, visited))
		}
	}

	return out
}

type traceSummaryInput struct {
	TraceID string `json:"trace_id"`
}

type traceSummaryOutput struct {
	TraceID     string     `json:"trace_id"`
	RequestID   string     `json:"request_id"`
	EventName   string     `json:"event_name,omitempty"`
	Flow        string     `json:"flow,omitempty"`
	LatencyMs   any        `json:"latency_ms,omitempty"`
	RootSpanIDs []string   `json:"root_span_ids,omitempty"`
	Paths       [][]string `json:"paths,omitempty"`
}

func handleTraceSummary(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceSummaryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.TraceID == "" {
		return nil, fmt.Errorf("trace_id required")
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)

	out := traceSummaryOutput{
		TraceID:   input.TraceID,
		RequestID: reqID,
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
		}
	}

	rootSpans := rootSpanIDsForTrace(g, reqID)
	out.RootSpanIDs = rootSpans
	out.Paths = spanPathsForRoots(g, rootSpans)

	if len(out.Paths) == 0 {
		if chain := serviceChainForRequest(g, reqID); len(chain) > 0 {
			out.Paths = [][]string{chain}
		}
	}

	return out, nil
}
