package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

type stubActiveSource struct{ incs []incidents.Incident }

func (s stubActiveSource) Active(_ context.Context) ([]incidents.Incident, error) {
	return s.incs, nil
}

func usersPtr(n int) *int { return &n }

func inc(id, env, svc, code string, users, reqs, svcs int, cause incidents.Cause, conf incidents.Confidence, suspect string) incidents.Incident {
	return incidents.Incident{
		IncidentID:       id,
		Env:              env,
		ErrorFamily:      apiv2.ErrorFamily{Service: svc, Step: "charge", ErrorCode: code},
		Status:           incidents.StatusActive,
		Cause:            cause,
		Confidence:       conf,
		AffectedUsers:    usersPtr(users),
		AffectedRequests: reqs,
		AffectedServices: svcs,
		SuspectDeployID:  suspect,
	}
}

// callList runs the tool and JSON round-trips the result, mirroring what the
// MCP/REST surfaces serialize (so generic map/slice assertions work).
func callList(t *testing.T, src tools.ActiveIncidentsSource, params string) map[string]any {
	t.Helper()
	reg := tools.NewRegistry()
	if err := tools.RegisterListActiveIncidentsTool(reg, src); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := reg.Call(context.Background(), "list_active_incidents", json.RawMessage(params))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestListActiveIncidentsRanksByImpact(t *testing.T) {
	src := stubActiveSource{incs: []incidents.Incident{
		inc("low", "prod", "a", "E1", 1, 5, 1, incidents.CauseDeploy, incidents.ConfidenceHigh, "dep_1"),
		inc("high", "prod", "b", "E2", 100, 200, 4, incidents.CauseDeploy, incidents.ConfidenceHigh, "dep_2"),
	}}
	res := callList(t, src, `{}`)
	rows := res["incidents"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// First row must be the higher-impact incident.
	first := rows[0].(map[string]any)
	if first["incident_id"] != "high" {
		t.Fatalf("ranking wrong; first = %v, want high-impact", first["incident_id"])
	}
}

func TestListActiveIncidentsNeedsJudgmentFlag(t *testing.T) {
	src := stubActiveSource{incs: []incidents.Incident{
		// Confident deploy with a suspect change → no judgment flag.
		inc("clear", "prod", "a", "E1", 10, 10, 1, incidents.CauseDeploy, incidents.ConfidenceHigh, "dep_1"),
		// Unknown cause → needs judgment.
		inc("unknown", "prod", "b", "E2", 9, 9, 1, incidents.CauseUnknown, incidents.ConfidenceLow, ""),
	}}
	res := callList(t, src, `{}`)
	flags := map[string]bool{}
	for _, r := range res["incidents"].([]interface{}) {
		m := r.(map[string]any)
		flags[m["incident_id"].(string)] = m["needs_judgment"].(bool)
	}
	if flags["clear"] {
		t.Fatalf("confident incident with suspect change should not need judgment")
	}
	if !flags["unknown"] {
		t.Fatalf("unknown/low-confidence/no-suspect incident must need judgment")
	}
}

func TestListActiveIncidentsEnvFilterAndLimit(t *testing.T) {
	src := stubActiveSource{incs: []incidents.Incident{
		inc("p1", "prod", "a", "E1", 5, 5, 1, incidents.CauseApp, incidents.ConfidenceMedium, "d"),
		inc("s1", "staging", "b", "E2", 9, 9, 1, incidents.CauseApp, incidents.ConfidenceMedium, "d"),
	}}
	res := callList(t, src, `{"env":"prod"}`)
	rows := res["incidents"].([]interface{})
	if len(rows) != 1 || rows[0].(map[string]any)["incident_id"] != "p1" {
		t.Fatalf("env filter failed: %+v", rows)
	}
	if got := callList(t, src, `{"limit":1}`)["count"]; got.(float64) != 1 {
		t.Fatalf("limit not applied: count=%v", got)
	}
}
