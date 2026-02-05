package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

type explainRequestInput struct {
	RequestID string `json:"request_id"`
	TraceID   string `json:"trace_id"`
}

type explainRequestOutput struct {
	RequestID    string   `json:"request_id"`
	LatencyMs    any      `json:"latency_ms,omitempty"`
	Flow         any      `json:"flow,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	UserTier     any      `json:"user_tier,omitempty"`
	FeatureFlags []string `json:"feature_flags,omitempty"`
	SpanID       string   `json:"span_id,omitempty"`
	SpanService  any      `json:"span_service,omitempty"`
	SpanDepth    string   `json:"span_depth,omitempty"`
	Service      any      `json:"service,omitempty"`
	ErrorCode    any      `json:"error_code,omitempty"`
	ErrorMsg     any      `json:"error_msg,omitempty"`
}

func handleExplainRequest(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input explainRequestInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.RequestID == "" && input.TraceID == "" {
		return nil, fmt.Errorf("request_id or trace_id required")
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = core.ID("request", input.TraceID)
	}
	g := store.Snapshot()
	ex, err := analysis.ExplainRequest(g, requestID)
	if err != nil {
		return nil, err
	}
	return explainRequestOutput{
		RequestID:    ex.RequestID,
		LatencyMs:    ex.LatencyMs,
		Flow:         ex.Flow,
		UserID:       ex.UserID,
		UserTier:     ex.UserTier,
		FeatureFlags: ex.FeatureFlags,
		SpanID:       ex.SpanID,
		SpanService:  ex.SpanService,
		SpanDepth:    ex.SpanDepth,
		Service:      ex.Service,
		ErrorCode:    ex.ErrorCode,
		ErrorMsg:     ex.ErrorMsg,
	}, nil
}

type insightsInput struct {
	Window      string `json:"window,omitempty"`
	TopErrors   int    `json:"top_errors,omitempty"`
	TopServices int    `json:"top_services,omitempty"`
}

type insightError struct {
	ErrorCode string `json:"error_code"`
	Count     int    `json:"count"`
}

type insightService struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
}

type insightsOutput struct {
	TotalFailures int              `json:"total_failures"`
	TopErrors     []insightError   `json:"top_errors,omitempty"`
	TopServices   []insightService `json:"top_services,omitempty"`
}

func handleInsights(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input insightsInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}

	if input.TopErrors == 0 {
		input.TopErrors = 5
	}
	if input.TopServices == 0 {
		input.TopServices = 5
	}

	g := store.Snapshot()

	if input.Window != "" {
		d, err := time.ParseDuration(input.Window)
		if err != nil {
			return nil, fmt.Errorf("invalid window: %w", err)
		}
		end := time.Now()
		start := end.Add(-d)
		sum := store.SummarizeWindow(start, end)

		errorCounts := map[string]int{}
		total := 0
		for errID, count := range sum.ErrorCount {
			code := errorCodeForID(g, errID)
			errorCounts[code] += count
			total += count
		}

		serviceCounts := map[string]int{}
		for svcID, errs := range sum.ServiceErrorCount {
			name := serviceNameForID(g, svcID)
			for _, count := range errs {
				serviceCounts[name] += count
			}
		}

		return insightsOutput{
			TotalFailures: total,
			TopErrors:     mapCountToTopErrors(errorCounts, input.TopErrors),
			TopServices:   mapCountToTopServices(serviceCounts, input.TopServices),
		}, nil
	}

	errorCounts := map[string]int{}
	serviceCounts := map[string]int{}
	total := 0

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		errNode := g.Nodes[e.To]
		code := ""
		if errNode.Attr != nil {
			code, _ = errNode.Attr["code"].(string)
		}
		if code == "" {
			code = e.To
		}
		errorCounts[code]++
		total++

		reqID := e.From
		if fromNode, ok := g.Nodes[e.From]; ok && fromNode.Type == core.NodeSpan {
			reqID = ""
			for _, re := range g.Edges {
				if re.Type == core.EdgeRequestHasSpan && re.To == e.From {
					reqID = re.From
					break
				}
			}
			if reqID == "" {
				continue
			}
		}

		for _, ed := range g.Edges {
			if ed.From == reqID && ed.Type == core.EdgeHandledBy {
				serviceCounts[serviceNameForID(g, ed.To)]++
			}
		}
	}

	return insightsOutput{
		TotalFailures: total,
		TopErrors:     mapCountToTopErrors(errorCounts, input.TopErrors),
		TopServices:   mapCountToTopServices(serviceCounts, input.TopServices),
	}, nil
}
