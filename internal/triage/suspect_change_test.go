package triage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func floatPtr(f float64) *float64 { return &f }

// stubIncidentReaderEvidence satisfies IncidentReader with a fixed incident.
type stubIncidentReaderEvidence struct{ inc incidents.Incident }

func (s stubIncidentReaderEvidence) Get(_ context.Context, _ string) (incidents.Incident, error) {
	return s.inc, nil
}

// TestSuspectDeployThreadedFromEvidence proves the classifier's correlated
// deployment (EvidenceDeployment) is the single source of truth surfaced to triage.
func TestSuspectDeployThreadedFromEvidence(t *testing.T) {
	inc := incidents.Incident{
		IncidentID: "inc_x",
		Evidence: []incidents.Evidence{
			{Kind: incidents.EvidenceTrace, TraceID: "t1"},
			{Kind: incidents.EvidenceDeployment, DeployID: "dep_42"},
		},
	}
	a := NewIncidentLookupAdapter(stubIncidentReaderEvidence{inc: inc})
	sum, err := a.GetIncident(context.Background(), "inc_x")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if sum.SuspectDeployID != "dep_42" {
		t.Fatalf("SuspectDeployID = %q, want dep_42", sum.SuspectDeployID)
	}
}

func TestGetIncidentPrefersPersistedSuspectDeployID(t *testing.T) {
	// Persisted field is authoritative even when the (cap-prone) evidence list
	// has no deployment row — this is what makes Suspect Change non-flickering.
	a := NewIncidentLookupAdapter(stubIncidentReaderEvidence{inc: incidents.Incident{
		IncidentID:      "inc_p",
		SuspectDeployID: "dep_persisted",
		Evidence:        []incidents.Evidence{{Kind: incidents.EvidenceRuntime, SignalID: "rt"}},
	}})
	sum, err := a.GetIncident(context.Background(), "inc_p")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if sum.SuspectDeployID != "dep_persisted" {
		t.Fatalf("SuspectDeployID = %q, want dep_persisted", sum.SuspectDeployID)
	}

	// And the persisted field wins over a stale evidence-derived id.
	a = NewIncidentLookupAdapter(stubIncidentReaderEvidence{inc: incidents.Incident{
		IncidentID:      "inc_p2",
		SuspectDeployID: "dep_new",
		Evidence:        []incidents.Evidence{{Kind: incidents.EvidenceDeployment, DeployID: "dep_old"}},
	}})
	sum, _ = a.GetIncident(context.Background(), "inc_p2")
	if sum.SuspectDeployID != "dep_new" {
		t.Fatalf("persisted field should win, got %q", sum.SuspectDeployID)
	}
}

func TestSuspectDeployEmptyWhenNoDeployEvidence(t *testing.T) {
	a := NewIncidentLookupAdapter(stubIncidentReaderEvidence{inc: incidents.Incident{
		IncidentID: "inc_y",
		Evidence:   []incidents.Evidence{{Kind: incidents.EvidenceTrace, TraceID: "t1"}},
	}})
	sum, err := a.GetIncident(context.Background(), "inc_y")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if sum.SuspectDeployID != "" {
		t.Fatalf("SuspectDeployID = %q, want empty", sum.SuspectDeployID)
	}
}

type stubDeployStore struct {
	rec       *DeployRecord
	byIDErr   error
	before    *float64
	after     *float64
	rateErr   error
	rateCalls int
}

func (s *stubDeployStore) DeploymentByID(_ context.Context, _ string) (*DeployRecord, error) {
	return s.rec, s.byIDErr
}

func (s *stubDeployStore) DeployErrorRate(_ context.Context, _ string, _ time.Time) (*float64, *float64, error) {
	s.rateCalls++
	return s.before, s.after, s.rateErr
}

func TestSuspectChangeAdapter_NilWhenNoCorrelation(t *testing.T) {
	a := NewSuspectChangeAdapter(&stubDeployStore{})
	sc, err := a.SuspectChange(context.Background(), IncidentSummary{}, BuildOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Fatalf("expected nil suspect change, got %+v", sc)
	}
}

func TestSuspectChangeAdapter_Hydrates(t *testing.T) {
	firstSeen := time.Date(2026, 5, 6, 0, 1, 0, 0, time.UTC)
	store := &stubDeployStore{
		rec: &DeployRecord{
			ID: "dep_42", Service: "payment", Version: "v1.4.2",
			CommitSHA: "a1b2c3d", PRURL: "https://example/pr/482", CommitAuthor: "alice",
			FirstSeen: firstSeen,
		},
		before: floatPtr(0.01), after: floatPtr(0.42),
	}
	a := NewSuspectChangeAdapter(store)
	sc, err := a.SuspectChange(context.Background(), IncidentSummary{SuspectDeployID: "dep_42"}, BuildOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc == nil {
		t.Fatal("expected suspect change, got nil")
	}
	if sc.DeployID != "dep_42" || sc.CommitSHA != "a1b2c3d" || sc.PRURL != "https://example/pr/482" || sc.CommitAuthor != "alice" {
		t.Fatalf("provenance wrong: %+v", sc)
	}
	if sc.ErrorRateBefore == nil || sc.ErrorRateAfter == nil || *sc.ErrorRateAfter != 0.42 {
		t.Fatalf("rates wrong: %+v", sc)
	}
	if sc.DeployedAt == "" {
		t.Fatalf("DeployedAt should be set from FirstSeen")
	}
}

func TestSuspectChangeAdapter_GracefulOnMissOrError(t *testing.T) {
	// Miss: DeploymentByID returns nil → nil, no error.
	a := NewSuspectChangeAdapter(&stubDeployStore{rec: nil})
	sc, err := a.SuspectChange(context.Background(), IncidentSummary{SuspectDeployID: "gone"}, BuildOptions{})
	if err != nil || sc != nil {
		t.Fatalf("miss should be (nil,nil), got sc=%+v err=%v", sc, err)
	}

	// Store error must not break triage.
	a = NewSuspectChangeAdapter(&stubDeployStore{byIDErr: context.DeadlineExceeded})
	sc, err = a.SuspectChange(context.Background(), IncidentSummary{SuspectDeployID: "dep"}, BuildOptions{})
	if err != nil || sc != nil {
		t.Fatalf("store error should degrade to (nil,nil), got sc=%+v err=%v", sc, err)
	}
}

// fixedSuspect returns a copy of a fixed SuspectChange, isolating callers.
type fixedSuspect struct{ sc *pkgtriage.SuspectChange }

func (f fixedSuspect) SuspectChange(_ context.Context, _ IncidentSummary, _ BuildOptions) (*pkgtriage.SuspectChange, error) {
	if f.sc == nil {
		return nil, nil
	}
	c := *f.sc
	return &c, nil
}

func buildWithSuspect(t *testing.T, sc *pkgtriage.SuspectChange, now time.Time) *pkgtriage.Report {
	t.Helper()
	deps := stubDeps()
	deps.Incidents = richIncidents{}
	deps.Deploy = fixedSuspect{sc: sc}
	deps.Now = func() time.Time { return now }
	eng, err := NewEngine(deps)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("15m", false, now)
	rep, err := eng.Build(context.Background(), "inc_abc", opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return rep
}

// TestSuspectChange_HashExcludesVolatileRates proves the determinism fix: two
// reports with the same suspect identity but different measured rates (and a
// minute apart) produce the same report_hash, while the rates still appear in JSON.
func TestSuspectChange_HashExcludesVolatileRates(t *testing.T) {
	base := pkgtriage.SuspectChange{DeployID: "dep_42", Service: "payment", Version: "v1.4.2", CommitSHA: "a1b2c3d"}

	a := base
	a.ErrorRateBefore, a.ErrorRateAfter = floatPtr(0.01), floatPtr(0.30)
	repA := buildWithSuspect(t, &a, time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))

	b := base
	b.ErrorRateBefore, b.ErrorRateAfter = floatPtr(0.01), floatPtr(0.55) // after-window still filling
	repB := buildWithSuspect(t, &b, time.Date(2026, 5, 6, 12, 1, 0, 0, time.UTC))

	if repA.ReportHash != repB.ReportHash {
		t.Fatalf("report_hash churned on volatile rates:\nA: %s\nB: %s", repA.ReportHash, repB.ReportHash)
	}
	raw, _ := json.Marshal(repA)
	if !strings.Contains(string(raw), "error_rate_after") {
		t.Fatalf("rates should still be present in JSON output: %s", raw)
	}

	// A different suspect deploy MUST change the hash.
	c := base
	c.DeployID = "dep_99"
	repC := buildWithSuspect(t, &c, time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	if repC.ReportHash == repA.ReportHash {
		t.Fatalf("report_hash should change when suspect deploy changes")
	}
}

// TestEngineBuildsWithoutDeployDep confirms the dependency is optional.
func TestEngineBuildsWithoutDeployDep(t *testing.T) {
	deps := stubDeps()
	deps.Incidents = richIncidents{}
	eng, err := NewEngine(deps) // no Deploy
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	opts, _ := ParseBuildOptions("15m", false, time.Now())
	rep, err := eng.Build(context.Background(), "inc_abc", opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rep.SuspectChange != nil {
		t.Fatalf("expected nil suspect change without Deploy dep")
	}
}
