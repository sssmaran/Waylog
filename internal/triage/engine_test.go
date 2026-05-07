package triage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestNewEngineRequiresAllDeps(t *testing.T) {
	if _, err := NewEngine(Deps{}); err == nil {
		t.Fatalf("expected error when deps are zero, got nil")
	}
}

func TestEngineBuildReturnsErrorForUnknownIncident(t *testing.T) {
	deps := stubDeps()
	deps.Incidents = stubIncidentLookup{err: ErrUnknownIncident}
	eng, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("", false, time.Now())
	if _, err := eng.Build(context.Background(), "inc_missing", opts); err == nil {
		t.Fatalf("expected error for unknown incident")
	}
}

// --- test helpers ---

type stubIncidentLookup struct {
	err error
}

func (s stubIncidentLookup) GetIncident(ctx context.Context, id string) (IncidentSummary, error) {
	return IncidentSummary{}, s.err
}

type stubBlastQuery struct{}

func (stubBlastQuery) BlastSnapshot(ctx context.Context, inc IncidentSummary, opts BuildOptions) (BlastSnapshotResult, error) {
	return BlastSnapshotResult{}, nil
}

type stubStoryBuilder struct{}

func (stubStoryBuilder) FirstFailureStory(ctx context.Context, inc IncidentSummary, opts BuildOptions) (FirstFailureResult, error) {
	return FirstFailureResult{}, nil
}

type stubSignalQuery struct{}

func (stubSignalQuery) SignalsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]SignalEvidence, error) {
	return nil, nil
}

type stubNextChecks struct{}

func (stubNextChecks) NextChecks(ctx context.Context, inc IncidentSummary) ([]NextCheckSpec, error) {
	return nil, nil
}

func stubDeps() Deps {
	return Deps{
		Incidents:  stubIncidentLookup{},
		Blast:      stubBlastQuery{},
		Story:      stubStoryBuilder{},
		Signals:    stubSignalQuery{},
		NextChecks: stubNextChecks{},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC) },
	}
}

type richBlast struct{}

func (richBlast) BlastSnapshot(ctx context.Context, inc IncidentSummary, opts BuildOptions) (BlastSnapshotResult, error) {
	return BlastSnapshotResult{
		Requests: 12, Users: 8, Services: 4,
		TopErrorFamilies: []pkgtriage.ErrorFamily{
			{Service: "payment", Step: "payment.charge", ErrorCode: "PMT_502", Count: 11},
		},
	}, nil
}

type richStory struct{}

func (richStory) FirstFailureStory(ctx context.Context, inc IncidentSummary, opts BuildOptions) (FirstFailureResult, error) {
	return FirstFailureResult{
		Payload:      json.RawMessage(`{"trace_id":"abc","first_failure":"payment.charge"}`),
		SampleTraces: []pkgtriage.TraceSample{{TraceID: "abc", Summary: "payment 502"}},
	}, nil
}

type richSignals struct{}

func (richSignals) SignalsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]SignalEvidence, error) {
	return []SignalEvidence{{ID: "sig_1", Type: "deploy", EvidenceIDs: []string{"e1"}}}, nil
}

type richNextChecks struct{}

func (richNextChecks) NextChecks(ctx context.Context, inc IncidentSummary) ([]NextCheckSpec, error) {
	return []NextCheckSpec{{ID: "check_payment_health", Prompt: "Verify payment-service health"}}, nil
}

type richIncidents struct{}

func (richIncidents) GetIncident(ctx context.Context, id string) (IncidentSummary, error) {
	return IncidentSummary{
		ID: id, Window: "15m", Env: "demo",
		StartedAt: time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 6, 0, 5, 0, 0, time.UTC),
		Service:   "payment", Step: "payment.charge", ErrorCode: "PMT_502",
		Confidence: pkgtriage.ConfidenceHigh,
		NextChecks: []string{"Verify payment-service health"},
	}, nil
}

func TestEngineBuildAssemblesAllSections(t *testing.T) {
	deps := Deps{
		Incidents:  richIncidents{},
		Blast:      richBlast{},
		Story:      richStory{},
		Signals:    richSignals{},
		NextChecks: richNextChecks{},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("15m", false, deps.Now())
	r, err := eng.Build(context.Background(), "inc_abc", opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if r.IncidentRef.Window != "15m0s" {
		t.Fatalf("incident_ref.window should reflect opts.Window, got %q", r.IncidentRef.Window)
	}
	if r.BlastSnapshot.Requests != 12 {
		t.Fatalf("blast.requests = %d, want 12", r.BlastSnapshot.Requests)
	}
	if len(r.SampleTraces) != 1 || r.SampleTraces[0].TraceID != "abc" {
		t.Fatalf("sample_traces wrong: %+v", r.SampleTraces)
	}
	if len(r.Signals) != 1 || r.Signals[0].Type != "deploy" {
		t.Fatalf("signals wrong: %+v", r.Signals)
	}
	if len(r.NextChecks) != 1 {
		t.Fatalf("next_checks missing")
	}
	if r.Confidence != pkgtriage.ConfidenceHigh {
		t.Fatalf("Confidence = %q, want high (must come from incident, not hard-coded medium)", r.Confidence)
	}
	if r.ReportHash == "" || !strings.HasPrefix(r.ReportHash, "sha256:") {
		t.Fatalf("report_hash missing/invalid: %q", r.ReportHash)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("produced report failed validation: %v", err)
	}
}

// TestTriageReportFromDemoShape exercises the engine end-to-end against the
// demo's actual shape — the cross-service signal (payment dependency on a
// checkout incident), high confidence, and incident-provided next checks.
// It is the regression gate for the four M1 fixes.
func TestTriageReportFromDemoShape(t *testing.T) {
	demoIncidents := stubGetIncident(IncidentSummary{
		ID:      "inc_demo",
		Window:  "15m",
		Env:     "demo",
		Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502",
		StartedAt:  time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 6, 0, 5, 0, 0, time.UTC),
		Confidence: pkgtriage.ConfidenceHigh,
		NextChecks: []string{"Verify payment-service health", "Check recent deploys"},
	})
	demoBlast := stubBlastSnapshot{
		out: BlastSnapshotResult{
			Requests: 7, Users: 3, Services: 2,
			TopErrorFamilies: []pkgtriage.ErrorFamily{
				{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502", Count: 6},
			},
		},
	}
	// Story payload mirrors apiv2.StoryResponse shape; engine treats it as
	// opaque RawMessage.
	demoStory := stubStoryResult{
		out: FirstFailureResult{
			Payload: json.RawMessage(`{"trace_id":"t_demo","anchor":{"step":"payment.charge","error_code":"PMT_502"},"path":[],"logs":[],"downstream":[],"linkage":"trace_id"}`),
			SampleTraces: []pkgtriage.TraceSample{
				{TraceID: "t_demo", Summary: "checkout PMT_502"},
			},
		},
	}
	// Cross-service signal: incident is on `checkout`, but the dependency
	// signal is from `payment`. Fix 1 ensures the broad query surfaces it.
	demoSignals := stubSignalsResult{
		out: []SignalEvidence{
			{ID: "sig_payment_dep", Type: "dependency", EvidenceIDs: []string{"sig_payment_dep"}},
		},
	}
	// Fix 3: NextChecks must come from the incident, not a static map keyed
	// by service+code.
	demoChecks := stubNextChecksResult{}

	deps := Deps{
		Incidents:  demoIncidents,
		Blast:      demoBlast,
		Story:      demoStory,
		Signals:    demoSignals,
		NextChecks: demoChecks,
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("15m", false, deps.Now())

	r, err := eng.Build(context.Background(), "inc_demo", opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.IncidentRef.ID != "inc_demo" {
		t.Fatalf("IncidentRef.ID = %q, want inc_demo", r.IncidentRef.ID)
	}
	foundFamily := false
	for _, fam := range r.BlastSnapshot.TopErrorFamilies {
		if fam.ErrorCode == "PMT_502" {
			foundFamily = true
		}
	}
	if !foundFamily {
		t.Fatalf("BlastSnapshot.TopErrorFamilies missing PMT_502: %+v", r.BlastSnapshot.TopErrorFamilies)
	}
	foundPaymentSig := false
	for _, sig := range r.Signals {
		if sig.ID == "sig_payment_dep" {
			foundPaymentSig = true
		}
	}
	if !foundPaymentSig {
		t.Fatalf("Signals missing payment dependency signal: %+v", r.Signals)
	}
	if r.Confidence != pkgtriage.ConfidenceHigh {
		t.Fatalf("Confidence = %q, want high", r.Confidence)
	}
	if len(r.NextChecks) != 2 {
		t.Fatalf("NextChecks len = %d, want 2: %+v", len(r.NextChecks), r.NextChecks)
	}
	if r.NextChecks[0].ID != "check_0" || r.NextChecks[0].Prompt != "Verify payment-service health" {
		t.Fatalf("NextChecks[0] = %+v, want {check_0, Verify payment-service health}", r.NextChecks[0])
	}
	if r.NextChecks[1].ID != "check_1" || r.NextChecks[1].Prompt != "Check recent deploys" {
		t.Fatalf("NextChecks[1] = %+v, want {check_1, Check recent deploys}", r.NextChecks[1])
	}
	if r.ReportHash == "" {
		t.Fatalf("ReportHash empty")
	}
}

// --- additional stubs used by the demo-shape regression test ---

type stubGetIncident IncidentSummary

func (s stubGetIncident) GetIncident(_ context.Context, _ string) (IncidentSummary, error) {
	return IncidentSummary(s), nil
}

type stubBlastSnapshot struct{ out BlastSnapshotResult }

func (s stubBlastSnapshot) BlastSnapshot(_ context.Context, _ IncidentSummary, _ BuildOptions) (BlastSnapshotResult, error) {
	return s.out, nil
}

type stubStoryResult struct{ out FirstFailureResult }

func (s stubStoryResult) FirstFailureStory(_ context.Context, _ IncidentSummary, _ BuildOptions) (FirstFailureResult, error) {
	return s.out, nil
}

type stubSignalsResult struct{ out []SignalEvidence }

func (s stubSignalsResult) SignalsFor(_ context.Context, _ IncidentSummary, _ BuildOptions) ([]SignalEvidence, error) {
	return s.out, nil
}

// stubNextChecksResult mirrors the production adapter: it consumes
// inc.NextChecks and converts them to NextCheckSpec entries with stable IDs.
type stubNextChecksResult struct{}

func (stubNextChecksResult) NextChecks(_ context.Context, inc IncidentSummary) ([]NextCheckSpec, error) {
	out := make([]NextCheckSpec, 0, len(inc.NextChecks))
	for i, prompt := range inc.NextChecks {
		out = append(out, NextCheckSpec{ID: nextCheckID(i), Prompt: prompt})
	}
	return out, nil
}

func nextCheckID(i int) string {
	return "check_" + itoa(i)
}

func itoa(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	}
	// Tests only exercise small indices.
	return "n"
}
