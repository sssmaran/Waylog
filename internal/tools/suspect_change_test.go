package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/triage"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

type stubSuspectDeploy struct{ sc *pkgtriage.SuspectChange }

func (s stubSuspectDeploy) SuspectChange(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) (*pkgtriage.SuspectChange, error) {
	return s.sc, nil
}

func newSuspectEngine(t *testing.T, sc *pkgtriage.SuspectChange) *triage.Engine {
	t.Helper()
	deps := triage.Deps{
		Incidents:  triageStubIncidents{},
		Blast:      triageStubBlast{},
		Story:      triageStubStory{},
		Signals:    triageStubSignals{},
		NextChecks: triageStubNextChecks{},
		Deploy:     stubSuspectDeploy{sc: sc},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := triage.NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

func TestRegisterSuspectChangeTool(t *testing.T) {
	reg := tools.NewRegistry()
	if err := tools.RegisterSuspectChangeTool(reg, newSuspectEngine(t, nil)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Tool("suspect_change"); !ok {
		t.Fatalf("suspect_change not registered")
	}
}

func TestSuspectChangeToolReturnsChange(t *testing.T) {
	reg := tools.NewRegistry()
	sc := &pkgtriage.SuspectChange{DeployID: "dep_42", Service: "payment", PRURL: "https://example/pr/482"}
	if err := tools.RegisterSuspectChangeTool(reg, newSuspectEngine(t, sc)); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := reg.Call(context.Background(), "suspect_change", json.RawMessage(`{"incident_id":"inc_abc"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	got, ok := out.(*pkgtriage.SuspectChange)
	if !ok {
		t.Fatalf("expected *pkgtriage.SuspectChange, got %T", out)
	}
	if got.DeployID != "dep_42" {
		t.Fatalf("wrong deploy id: %q", got.DeployID)
	}
}

func TestSuspectChangeToolNotFound(t *testing.T) {
	reg := tools.NewRegistry()
	if err := tools.RegisterSuspectChangeTool(reg, newSuspectEngine(t, nil)); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := reg.Call(context.Background(), "suspect_change", json.RawMessage(`{"incident_id":"inc_abc"}`))
	te, ok := tools.AsToolError(err)
	if !ok {
		t.Fatalf("expected ToolError, got %v", err)
	}
	if te.Code != tools.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %s", te.Code)
	}
}
