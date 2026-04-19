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
	SchemaVersion string                 `json:"schema_version"`
	RequestID     string                 `json:"request_id"`
	LatencyMs     any                    `json:"latency_ms,omitempty"`
	Flow          any                    `json:"flow,omitempty"`
	UserID        string                 `json:"user_id,omitempty"`
	UserTier      any                    `json:"user_tier,omitempty"`
	FeatureFlags  []string               `json:"feature_flags,omitempty"`
	SpanID        string                 `json:"span_id,omitempty"`
	SpanService   any                    `json:"span_service,omitempty"`
	SpanDepth     string                 `json:"span_depth,omitempty"`
	Service       any                    `json:"service,omitempty"`
	ErrorCode     any                    `json:"error_code,omitempty"`
	ErrorMsg      any                    `json:"error_msg,omitempty"`
	SpanChain     []analysis.SpanSummary `json:"span_chain,omitempty"`
}

func handleExplainRequest(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input explainRequestInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.RequestID == "" && input.TraceID == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "request_id or trace_id required"}
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = core.ID("request", input.TraceID)
	}
	g := store.Snapshot()
	ex, err := analysis.ExplainRequestWithTrace(g, traceStoreFrom(store), requestID)
	if err != nil {
		return nil, &ToolError{Code: CodeNotFound, Message: err.Error()}
	}
	return explainRequestOutput{
		SchemaVersion: "1.0",
		RequestID:     ex.RequestID,
		LatencyMs:     ex.LatencyMs,
		Flow:          ex.Flow,
		UserID:        ex.UserID,
		UserTier:      ex.UserTier,
		FeatureFlags:  ex.FeatureFlags,
		SpanID:        ex.SpanID,
		SpanService:   ex.SpanService,
		SpanDepth:     ex.SpanDepth,
		Service:       ex.Service,
		ErrorCode:     ex.ErrorCode,
		ErrorMsg:      ex.ErrorMsg,
		SpanChain:     ex.SpanChain,
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
	SchemaVersion string           `json:"schema_version"`
	TotalFailures int              `json:"total_failures"`
	TopErrors     []insightError   `json:"top_errors,omitempty"`
	TopServices   []insightService `json:"top_services,omitempty"`
}

func handleInsights(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input insightsInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
		}
	}

	if input.TopErrors == 0 {
		input.TopErrors = 5
	}
	if input.TopServices == 0 {
		input.TopServices = 5
	}

	g := store.Snapshot()
	end := time.Now()
	var start time.Time
	if input.Window != "" {
		d, err := time.ParseDuration(input.Window)
		if err != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid window: %v", err)}
		}
		start = end.Add(-d)
	}

	sum := analysis.RollupWindow(g, store, traceStoreFrom(store), start, end)
	return insightsOutput{
		SchemaVersion: "1.0",
		TotalFailures: sum.TotalFailures,
		TopErrors:     mapCountToTopErrors(sum.PrimaryErrorCount, input.TopErrors),
		TopServices:   mapCountToTopServices(sum.ServiceFailureCount, input.TopServices),
	}, nil
}
