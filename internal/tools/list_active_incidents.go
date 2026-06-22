package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
)

// ActiveIncidentsSource returns the engine's active/recovering incidents.
// *incidents.Engine satisfies it via Active.
type ActiveIncidentsSource interface {
	Active(ctx context.Context) ([]incidents.Incident, error)
}

const listActiveIncidentsInputSchema = `{
  "type": "object",
  "properties": {
    "env":   {"type": "string", "description": "Optional environment filter"},
    "limit": {"type": "integer", "description": "Max incidents to return (default 50)"}
  }
}`

const listActiveIncidentsOutputSchema = `{
  "type": "object",
  "description": "Active incidents ranked by impact; needs_judgment flags low-confidence/unknown/no-suspect-change."
}`

// activeIncidentRow is the compact, queue-shaped projection of an incident.
type activeIncidentRow struct {
	IncidentID       string `json:"incident_id"`
	Env              string `json:"env"`
	Service          string `json:"service"`
	Step             string `json:"step"`
	ErrorCode        string `json:"error_code"`
	Status           string `json:"status"`
	Cause            string `json:"cause"`
	Confidence       string `json:"confidence"`
	AffectedUsers    int    `json:"affected_users"`
	AffectedRequests int    `json:"affected_requests"`
	AffectedServices int    `json:"affected_services"`
	NeedsJudgment    bool   `json:"needs_judgment"`
	StartedAt        string `json:"started_at"`
}

// RegisterListActiveIncidentsTool exposes the on-call queue (Workflow 1) over the
// tool surface so MCP-only agents don't need the read API. Deterministic: ranked
// by impact (users → requests → services), then incident_id. needs_judgment marks
// the incidents where human investigation adds most: cause=unknown, low
// confidence, or no correlated suspect change.
func RegisterListActiveIncidentsTool(reg *Registry, src ActiveIncidentsSource) error {
	return reg.Register(Tool{
		Name:         "list_active_incidents",
		Description:  "List active/recovering incidents ranked by impact, flagging the ones that need human judgment.",
		Version:      "list-incidents.v1",
		InputSchema:  json.RawMessage(listActiveIncidentsInputSchema),
		OutputSchema: json.RawMessage(listActiveIncidentsOutputSchema),
		Examples: []string{
			`{}`,
			`{"env":"prod","limit":20}`,
		},
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			var p struct {
				Env   string `json:"env"`
				Limit int    `json:"limit"`
			}
			if len(params) > 0 {
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, fmt.Errorf("list_active_incidents: bad params: %w", err)
				}
			}
			if p.Limit <= 0 {
				p.Limit = 50
			}
			incs, err := src.Active(ctx)
			if err != nil {
				return nil, err
			}
			rows := make([]activeIncidentRow, 0, len(incs))
			for _, inc := range incs {
				if p.Env != "" && inc.Env != p.Env {
					continue
				}
				rows = append(rows, projectActiveIncident(inc))
			}
			sort.SliceStable(rows, func(i, j int) bool { return rankImpact(rows[i], rows[j]) })
			if len(rows) > p.Limit {
				rows = rows[:p.Limit]
			}
			return map[string]any{"incidents": rows, "count": len(rows)}, nil
		},
	})
}

func projectActiveIncident(inc incidents.Incident) activeIncidentRow {
	users := 0
	if inc.AffectedUsers != nil {
		users = *inc.AffectedUsers
	}
	return activeIncidentRow{
		IncidentID:       inc.IncidentID,
		Env:              inc.Env,
		Service:          inc.ErrorFamily.Service,
		Step:             inc.ErrorFamily.Step,
		ErrorCode:        inc.ErrorFamily.ErrorCode,
		Status:           string(inc.Status),
		Cause:            string(inc.Cause),
		Confidence:       string(inc.Confidence),
		AffectedUsers:    users,
		AffectedRequests: inc.AffectedRequests,
		AffectedServices: inc.AffectedServices,
		NeedsJudgment:    inc.Cause == incidents.CauseUnknown || inc.Confidence == incidents.ConfidenceLow || inc.SuspectDeployID == "",
		StartedAt:        inc.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// rankImpact orders by affected users, then requests, then services (all desc),
// then incident_id asc for a stable, deterministic queue.
func rankImpact(a, b activeIncidentRow) bool {
	if a.AffectedUsers != b.AffectedUsers {
		return a.AffectedUsers > b.AffectedUsers
	}
	if a.AffectedRequests != b.AffectedRequests {
		return a.AffectedRequests > b.AffectedRequests
	}
	if a.AffectedServices != b.AffectedServices {
		return a.AffectedServices > b.AffectedServices
	}
	return a.IncidentID < b.IncidentID
}
