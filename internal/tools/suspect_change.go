package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/triage"
)

const suspectChangeInputSchema = `{
  "type": "object",
  "required": ["incident_id"],
  "properties": {
    "incident_id": {"type": "string"},
    "window":      {"type": "string", "description": "Go duration string, default 15m"},
    "snapshot":    {"type": "boolean", "description": "Freeze evaluation bounds to incident.started_at..updated_at"}
  }
}`

const suspectChangeOutputSchema = `{
  "type": "object",
  "description": "SuspectChange; see pkg/triage.SuspectChange for the full Go struct."
}`

// RegisterSuspectChangeTool exposes the deployment/PR correlated to an incident.
// Backed by the same triage.Engine as triage_incident so there is one data path;
// returns the report's deterministic SuspectChange, or NOT_FOUND when the
// classifier correlated no deploy.
func RegisterSuspectChangeTool(reg *Registry, engine *triage.Engine) error {
	return reg.Register(Tool{
		Name:         "suspect_change",
		Description:  "Return the deployment/PR the incident classifier correlated to an incident (deterministic).",
		Version:      "suspect.v1",
		InputSchema:  json.RawMessage(suspectChangeInputSchema),
		OutputSchema: json.RawMessage(suspectChangeOutputSchema),
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
				return nil, fmt.Errorf("suspect_change: bad params: %w", err)
			}
			if p.IncidentID == "" {
				return nil, &ToolError{Code: CodeInvalidParams, Message: "incident_id required"}
			}
			opts, err := triage.ParseBuildOptions(p.Window, p.Snapshot, time.Now())
			if err != nil {
				return nil, err
			}
			rep, err := engine.Build(ctx, p.IncidentID, opts)
			if err != nil {
				return nil, err
			}
			if rep.SuspectChange == nil {
				return nil, &ToolError{Code: CodeNotFound, Message: "no deploy correlated to incident " + p.IncidentID}
			}
			return rep.SuspectChange, nil
		},
	})
}
