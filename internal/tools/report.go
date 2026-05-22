package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/reports"
	"github.com/sssmaran/WaylogCLI/internal/triage"
)

const renderTriageReportInputSchema = `{
  "type": "object",
  "required": ["incident_id"],
  "properties": {
    "incident_id": {"type": "string"},
    "format":      {"type": "string", "enum": ["markdown", "slack", "pagerduty"], "default": "markdown"},
    "window":      {"type": "string", "description": "Go duration string, default 15m"},
    "snapshot":    {"type": "boolean"}
  }
}`

const renderTriageReportOutputSchema = `{
  "type": "object",
  "required": ["format", "content_type", "body"],
  "properties": {
    "format": {"type": "string"},
    "content_type": {"type": "string"},
    "body": {}
  }
}`

func RegisterTriageReportTool(reg *Registry, engine *triage.Engine) error {
	return reg.Register(Tool{
		Name:         "render_triage_report",
		Description:  "Render a deterministic operator report from a TriageReport.",
		Version:      "triage-report.v1",
		InputSchema:  json.RawMessage(renderTriageReportInputSchema),
		OutputSchema: json.RawMessage(renderTriageReportOutputSchema),
		Examples: []string{
			`{"incident_id":"inc_01HX...","format":"markdown","snapshot":true}`,
			`{"incident_id":"inc_01HX...","format":"slack"}`,
		},
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			var p struct {
				IncidentID string `json:"incident_id"`
				Format     string `json:"format"`
				Window     string `json:"window"`
				Snapshot   bool   `json:"snapshot"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("render_triage_report: bad params: %w", err)
			}
			if p.IncidentID == "" {
				return nil, fmt.Errorf("render_triage_report: incident_id required")
			}
			opts, err := triage.ParseBuildOptions(p.Window, p.Snapshot, time.Now())
			if err != nil {
				return nil, err
			}
			rep, err := engine.Build(ctx, p.IncidentID, opts)
			if err != nil {
				return nil, err
			}
			return reports.Render(rep, p.Format)
		},
	})
}
