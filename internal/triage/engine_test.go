package triage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
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

// --- snapshot-projection tests (Task 13) ---

// fixedLookup returns a fixed IncidentSummary regardless of ID.
type fixedLookup struct{ inc IncidentSummary }

func (s fixedLookup) GetIncident(_ context.Context, _ string) (IncidentSummary, error) {
	return s.inc, nil
}

// noOpBlastReader satisfies BlastReader with zero results. Both production
// methods are exercised by blastQueryAdapter: when Blast.Latest is set, the
// adapter still calls Errors() to compute TopErrorFamilies.
type noOpBlastReader struct{}

func (noOpBlastReader) BlastRadius(_ incidents.SearchFilter, _ apiv2.BlastKey) apiv2.BlastRadiusResponse {
	return apiv2.BlastRadiusResponse{}
}
func (noOpBlastReader) Errors(_ incidents.SearchFilter, _ int) incidents.ErrorsResult {
	return incidents.ErrorsResult{}
}

// noOpIncidentReader satisfies IncidentReader with ErrNotFound. The story
// adapter's reader-driven path is gated behind Propagation.Latest == nil; when
// the projection runs, this is never called.
type noOpIncidentReader struct{}

func (noOpIncidentReader) Get(_ context.Context, _ string) (incidents.Incident, error) {
	return incidents.Incident{}, incidents.ErrNotFound
}

type stubAlertsResult struct{ out []pkgtriage.AlertRef }

func (s stubAlertsResult) AlertsFor(_ context.Context, _ IncidentSummary, _ BuildOptions) ([]pkgtriage.AlertRef, error) {
	return s.out, nil
}

func makeFixedSummary(t *testing.T, ts, firstSeen time.Time) IncidentSummary {
	t.Helper()
	users := 47
	return IncidentSummary{
		ID:         "inc_golden",
		Window:     "15m",
		Env:        "demo",
		StartedAt:  ts,
		UpdatedAt:  ts,
		Service:    "payment-service",
		Step:       "charge",
		ErrorCode:  "DB_TIMEOUT",
		Confidence: pkgtriage.ConfidenceMedium,
		NextChecks: []string{"Verify payment-service health"},
		Propagation: &incidents.PropagationSnapshot{
			Latest: &incidents.PropagationEvidence{
				OriginService: "payment-service",
				OriginStep:    "charge",
				Path: []incidents.PropagationStep{
					{Service: "payment-service", Step: "validate", Status: "ok", StartMS: 0, DurationMS: 5},
					{Service: "payment-service", Step: "charge", Status: "error", ErrorCode: "DB_TIMEOUT", StartMS: 5, DurationMS: 50},
				},
				SampleTraceID: "7a3fb2",
				FirstSeenAt:   &firstSeen,
				CapturedAt:    ts,
				CaptureStatus: incidents.CaptureOK,
			},
		},
		Blast: &incidents.BlastSnapshot{
			Latest: &incidents.BlastEvidence{
				AffectedRequests: 184,
				AffectedUsers:    &users,
				AffectedServices: 3,
				TopServices:      []string{"checkout", "api-gateway", "mobile-api"},
				SampledTraces:    []string{"7a3fb2", "1c4d5e", "9f8a7b"},
				CapturedAt:       ts,
				CaptureStatus:    incidents.CaptureOK,
			},
		},
	}
}

func newSnapshotProjectionEngine(t *testing.T, inc IncidentSummary, now time.Time) *Engine {
	t.Helper()
	eng, err := NewEngine(Deps{
		Incidents:  fixedLookup{inc: inc},
		Blast:      NewBlastQueryAdapter(noOpBlastReader{}),
		Story:      NewStoryBuilderAdapter(noOpIncidentReader{}, func(_ string) (apiv2.StoryResponse, bool) { return apiv2.StoryResponse{}, false }),
		Signals:    stubSignalsResult{},
		Alerts:     stubAlertsResult{},
		NextChecks: stubNextChecksResult{},
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng
}

func TestEngine_Build_GoldenHash_FromIncidentSnapshots(t *testing.T) {
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	firstSeen := ts.Add(-30 * time.Second)
	inc := makeFixedSummary(t, ts, firstSeen)
	eng := newSnapshotProjectionEngine(t, inc, ts)

	rpt, err := eng.Build(context.Background(), inc.ID, BuildOptions{Window: 15 * time.Minute})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const want = "sha256:857bb8f5682044d0cd80e8d26d79cf946a3ff9f355b85e122e78c77a0d7af572"
	if rpt.ReportHash != want {
		t.Fatalf("ReportHash = %s\nwant       = %s\n\n(If this is the first run, copy the actual hash above into the const.)", rpt.ReportHash, want)
	}
}

func TestEngine_Build_GoldenHash_OpeningNotInHashSurface(t *testing.T) {
	// Same Latest, different Opening — hash must not change. Opening is not
	// projected into the Report; only Latest is.
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	firstSeen := ts.Add(-30 * time.Second)
	incA := makeFixedSummary(t, ts, firstSeen)
	openUsers := 1
	incB := makeFixedSummary(t, ts, firstSeen)
	incB.Blast.Opening = &incidents.BlastEvidence{
		AffectedRequests: 1,
		AffectedUsers:    &openUsers,
		AffectedServices: 1,
		TopServices:      []string{"early"},
		SampledTraces:    []string{"early_trace"},
		CapturedAt:       ts.Add(-time.Minute),
		CaptureStatus:    incidents.CaptureOK,
	}

	engA := newSnapshotProjectionEngine(t, incA, ts)
	rptA, err := engA.Build(context.Background(), incA.ID, BuildOptions{Window: 15 * time.Minute})
	if err != nil {
		t.Fatalf("Build A: %v", err)
	}

	engB := newSnapshotProjectionEngine(t, incB, ts)
	rptB, err := engB.Build(context.Background(), incB.ID, BuildOptions{Window: 15 * time.Minute})
	if err != nil {
		t.Fatalf("Build B: %v", err)
	}
	if rptA.ReportHash != rptB.ReportHash {
		t.Fatalf("ReportHash differs but only Opening did:\nA: %s\nB: %s", rptA.ReportHash, rptB.ReportHash)
	}
}

func TestEngine_Build_ProjectionIsByteStable(t *testing.T) {
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	firstSeen := ts.Add(-30 * time.Second)
	inc := makeFixedSummary(t, ts, firstSeen)
	eng := newSnapshotProjectionEngine(t, inc, ts)

	rpt1, err := eng.Build(context.Background(), inc.ID, BuildOptions{Window: 15 * time.Minute})
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	rpt2, err := eng.Build(context.Background(), inc.ID, BuildOptions{Window: 15 * time.Minute})
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	j1, _ := json.Marshal(rpt1)
	j2, _ := json.Marshal(rpt2)
	if string(j1) != string(j2) {
		t.Fatalf("Report projection drifted between runs:\nfirst:  %s\nsecond: %s", j1, j2)
	}
	if rpt1.ReportHash != rpt2.ReportHash {
		t.Fatalf("ReportHash drifted: %s vs %s", rpt1.ReportHash, rpt2.ReportHash)
	}
}
