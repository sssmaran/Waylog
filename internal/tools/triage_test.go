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

func TestRegisterTriageToolListsTool(t *testing.T) {
	reg := tools.NewRegistry()
	eng := newStubEngine(t)
	if err := tools.RegisterTriageTool(reg, eng); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Tool("triage_incident"); !ok {
		t.Fatalf("triage_incident not registered")
	}
}

func TestTriageToolHandlerReturnsReport(t *testing.T) {
	reg := tools.NewRegistry()
	eng := newStubEngine(t)
	if err := tools.RegisterTriageTool(reg, eng); err != nil {
		t.Fatalf("register: %v", err)
	}
	params := json.RawMessage(`{"incident_id":"inc_abc","window":"15m","snapshot":false}`)
	out, err := reg.Call(context.Background(), "triage_incident", params)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	rep, ok := out.(*pkgtriage.Report)
	if !ok {
		t.Fatalf("expected *pkgtriage.Report, got %T", out)
	}
	if rep.IncidentRef.ID != "inc_abc" {
		t.Fatalf("wrong incident id: %q", rep.IncidentRef.ID)
	}
}

// newStubEngine wires a triage.Engine with stub deps that always succeed.
// We duplicate the stubs inline to avoid creating a separate `triagetest` helper package for M1.
func newStubEngine(t *testing.T) *triage.Engine {
	t.Helper()
	deps := triage.Deps{
		Incidents:  triageStubIncidents{},
		Blast:      triageStubBlast{},
		Story:      triageStubStory{},
		Signals:    triageStubSignals{},
		NextChecks: triageStubNextChecks{},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := triage.NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

type triageStubIncidents struct{}

func (triageStubIncidents) GetIncident(ctx context.Context, id string) (triage.IncidentSummary, error) {
	return triage.IncidentSummary{ID: id, Window: "15m", Confidence: pkgtriage.ConfidenceMedium}, nil
}

type triageStubBlast struct{}

func (triageStubBlast) BlastSnapshot(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) (triage.BlastSnapshotResult, error) {
	return triage.BlastSnapshotResult{}, nil
}

type triageStubStory struct{}

func (triageStubStory) FirstFailureStory(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) (triage.FirstFailureResult, error) {
	return triage.FirstFailureResult{}, nil
}

type triageStubSignals struct{}

func (triageStubSignals) SignalsFor(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) ([]triage.SignalEvidence, error) {
	return nil, nil
}

type triageStubNextChecks struct{}

func (triageStubNextChecks) NextChecks(ctx context.Context, inc triage.IncidentSummary) ([]triage.NextCheckSpec, error) {
	return nil, nil
}
