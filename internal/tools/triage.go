package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/triage"
)

const triageInputSchema = `{
  "type": "object",
  "required": ["incident_id"],
  "properties": {
    "incident_id": {"type": "string"},
    "window":      {"type": "string", "description": "Go duration string, default 15m"},
    "snapshot":    {"type": "boolean", "description": "Freeze evaluation bounds to incident.started_at..updated_at"}
  }
}`

const triageOutputSchema = `{
  "type": "object",
  "description": "TriageReport v1; see pkg/triage.Report for the full Go struct."
}`

func RegisterTriageTool(reg *Registry, engine *triage.Engine) error {
	return reg.Register(Tool{
		Name:         "triage_incident",
		Description:  "Build a deterministic TriageReport for an open incident.",
		Version:      "triage.v1",
		InputSchema:  json.RawMessage(triageInputSchema),
		OutputSchema: json.RawMessage(triageOutputSchema),
		Examples: []string{
			`{"incident_id":"inc_01HX...","window":"15m"}`,
			`{"incident_id":"inc_01HX...","snapshot":true}`,
		},
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			var p struct {
				IncidentID string `json:"incident_id"`
				Window     string `json:"window"`
				Snapshot   bool   `json:"snapshot"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("triage_incident: bad params: %w", err)
			}
			if p.IncidentID == "" {
				return nil, fmt.Errorf("triage_incident: incident_id required")
			}
			opts, err := triage.ParseBuildOptions(p.Window, p.Snapshot, time.Now())
			if err != nil {
				return nil, err
			}
			return engine.Build(ctx, p.IncidentID, opts)
		},
	})
}
