package triage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

// buildFixtureReport builds a report from the in-memory rich* fixtures.
// No server, no live incident — the spec's fixture/in-memory path.
func buildFixtureReport(t *testing.T) *pkgtriage.Report {
	t.Helper()
	deps := Deps{
		Incidents: richIncidents{}, Blast: richBlast{}, Story: richStory{},
		Signals: richSignals{}, NextChecks: richNextChecks{},
		Now: func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
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
	return r
}

// Invariant (b1): provenance fields must not enter the canonical hash.
func TestHashExcludesProvenanceFields(t *testing.T) {
	r := buildFixtureReport(t)
	base, err := r.CanonicalHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	r.GeneratedAt = "2099-01-01T00:00:00Z"
	r.PlanRunID = "plan_zzz"
	r.ReportHash = "sha256:deadbeef"
	got, err := r.CanonicalHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got != base {
		t.Fatalf("provenance fields must not affect hash: base=%q got=%q", base, got)
	}
}

// Invariant (b2): the evidence projections that actually reach the report
// (runtime and alert) must drop CapturedAt, so a fresh capture can't churn
// report_hash. Blast and propagation snapshot CapturedAt never reach the report
// by construction (the report carries no propagation, and its blast comes from
// the BlastQuery, not the incident's blast snapshot), so there is nothing to
// assert for those two here.
func TestRuntimeProjectionDropsCapturedAt(t *testing.T) {
	mk := func(capturedAt time.Time) *incidents.RuntimeSnapshot {
		ev := incidents.RuntimeEvidence{
			Subtype:    "oom_killed",
			Service:    "checkout",
			Source:     "k8s",
			Severity:   "critical",
			Reason:     "OOMKilled",
			SignalID:   "sig_1",
			OccurredAt: time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC),
			CapturedAt: capturedAt,
		}
		return &incidents.RuntimeSnapshot{Matches: []incidents.RuntimeEvidence{ev}, Latest: &ev}
	}
	a := runtimeFromSnapshot(mk(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)))
	b := runtimeFromSnapshot(mk(time.Date(2026, 5, 6, 18, 30, 0, 0, time.UTC)))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("CapturedAt must not affect runtime projection: %+v vs %+v", a, b)
	}
}

// Invariant (b2, alert): the alert projection must also drop CapturedAt.
func TestAlertProjectionDropsCapturedAt(t *testing.T) {
	mk := func(capturedAt time.Time) *incidents.AlertSnapshot {
		return &incidents.AlertSnapshot{Latest: &incidents.AlertEvidence{
			CapturedAt:    capturedAt,
			CaptureStatus: incidents.CaptureOK,
			Matches: []incidents.MatchedAlert{{
				SignalID: "sig_1", Source: "alertmanager", Severity: "critical", Reason: "service down",
			}},
		}}
	}
	a, aok := alertsFromSnapshot(mk(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)))
	b, bok := alertsFromSnapshot(mk(time.Date(2026, 5, 6, 18, 30, 0, 0, time.UTC)))
	if !aok || !bok {
		t.Fatalf("alertsFromSnapshot should report fromSnapshot=true (a=%v b=%v)", aok, bok)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("CapturedAt must not affect alert projection: %+v vs %+v", a, b)
	}
}

// Invariant (c): material fields must change the canonical hash.
func TestHashIncludesMaterialFields(t *testing.T) {
	base, err := buildFixtureReport(t).CanonicalHash()
	if err != nil {
		t.Fatalf("base hash: %v", err)
	}
	cases := map[string]func(*pkgtriage.Report){
		"blast_requests": func(x *pkgtriage.Report) { x.BlastSnapshot.Requests++ },
		"top_error_family": func(x *pkgtriage.Report) {
			x.BlastSnapshot.TopErrorFamilies = append(x.BlastSnapshot.TopErrorFamilies,
				pkgtriage.ErrorFamily{Service: "svc", Step: "step", ErrorCode: "X", Count: 1})
		},
		"next_check": func(x *pkgtriage.Report) {
			x.NextChecks = append(x.NextChecks, pkgtriage.NextCheck{ID: "n_new", Prompt: "new"})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := buildFixtureReport(t)
			mutate(r)
			got, err := r.CanonicalHash()
			if err != nil {
				t.Fatalf("hash: %v", err)
			}
			if got == base {
				t.Fatalf("mutating %s must change the hash but did not", name)
			}
		})
	}
}

// Invariant (d): canonical hash is repeatable — identical across repeated calls
// on the same report. Report has no map fields, so there is no map-key ordering
// to canonicalize; this guards marshal determinism, not a deeper canonical-key
// normalization.
func TestCanonicalHashIsRepeatable(t *testing.T) {
	r := buildFixtureReport(t)
	first, err := r.CanonicalHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	for i := 0; i < 100; i++ {
		got, err := r.CanonicalHash()
		if err != nil {
			t.Fatalf("hash iter %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("canonical hash unstable at iter %d: %q vs %q", i, got, first)
		}
	}
}
