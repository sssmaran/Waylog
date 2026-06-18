package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

const (
	toolExplainReqName = "explain_request"
	toolBlastName      = "blast_radius"
)

// RegisterExplainRequestTool registers the explain_request tool backed by
// incidents.Reader. Output shape is apiv2.StoryResponse — see
// docs/superpowers/specs/2026-05-18-graph-to-incident-evidence-design.md.
func RegisterExplainRequestTool(reg *Registry, reader incidents.Reader) error {
	return reg.Register(Tool{
		Name:        toolExplainReqName,
		Description: "Return the trace story (per-step path, anchor, downstream) for a given trace_id.",
		Version:     "explain.v2",
		InputSchema: json.RawMessage(explainRequestV2InputSchema),
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p struct {
				TraceID string `json:"trace_id"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("explain_request: bad params: %w", err)
			}
			if p.TraceID == "" {
				return nil, fmt.Errorf("explain_request: trace_id required")
			}
			story, ok := reader.TraceStoryByTraceID(p.TraceID)
			if !ok {
				return nil, fmt.Errorf("explain_request: trace not found: %s", p.TraceID)
			}
			return story, nil
		},
		Examples: []string{"explain request <trace-id>"},
	})
}

// RegisterBlastRadiusTool registers the blast_radius tool backed by
// incidents.Reader. Output shape is apiv2.BlastRadiusResponse.
func RegisterBlastRadiusTool(reg *Registry, reader incidents.Reader) error {
	return reg.Register(Tool{
		Name:        toolBlastName,
		Description: "Aggregate impact (affected requests, users, services, top services, sample traces) for an error family in a window.",
		Version:     "blast.v2",
		InputSchema: json.RawMessage(blastRadiusV2InputSchema),
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p struct {
				Service   string `json:"service"`
				Step      string `json:"step"`
				ErrorCode string `json:"error_code"`
				Window    string `json:"window"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("blast_radius: bad params: %w", err)
			}
			if p.Service == "" || p.Step == "" || p.ErrorCode == "" {
				return nil, fmt.Errorf("blast_radius: service, step, error_code all required")
			}
			windowStr := p.Window
			if windowStr == "" {
				windowStr = "15m"
			}
			window, err := time.ParseDuration(windowStr)
			if err != nil {
				return nil, fmt.Errorf("blast_radius: bad window: %w", err)
			}
			now := time.Now()
			res := reader.BlastRadius(
				incidents.SearchFilter{Since: now.Add(-window), Until: now},
				apiv2.BlastKey{Service: p.Service, Step: p.Step, ErrorCode: p.ErrorCode},
			)
			return res, nil
		},
		Examples: []string{"blast radius for payment-service/charge/DB_TIMEOUT in 15m"},
	})
}

const explainRequestV2InputSchema = `{
  "type": "object",
  "required": ["trace_id"],
  "properties": {
    "trace_id": { "type": "string" }
  },
  "additionalProperties": false
}`

const blastRadiusV2InputSchema = `{
  "type": "object",
  "required": ["service", "step", "error_code"],
  "properties": {
    "service":    { "type": "string" },
    "step":       { "type": "string" },
    "error_code": { "type": "string" },
    "window":     { "type": "string", "description": "Go duration (default 15m)" }
  },
  "additionalProperties": false
}`
