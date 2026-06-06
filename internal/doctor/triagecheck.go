package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/triage"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

// checkTriageHash dogfoods determinism (v0.1.1 item 1): it builds a triage
// report twice from fixed, in-memory canned dependencies and asserts the
// report_hash is identical. No server, no live incident, no network.
func checkTriageHash() Check {
	build := func() (*pkgtriage.Report, error) {
		eng, err := triage.NewEngine(triage.Deps{
			Incidents:  cannedIncidents{},
			Blast:      cannedBlast{},
			Story:      cannedStory{},
			Signals:    cannedSignals{},
			NextChecks: cannedNextChecks{},
			Now:        func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		})
		if err != nil {
			return nil, err
		}
		opts, err := triage.ParseBuildOptions("15m", false, time.Unix(0, 0).UTC())
		if err != nil {
			return nil, err
		}
		return eng.Build(context.Background(), "inc_doctor", opts)
	}
	a, err := build()
	if err != nil {
		return Check{Name: "triage-hash", Status: StatusFail, Detail: err.Error()}
	}
	b, err := build()
	if err != nil {
		return Check{Name: "triage-hash", Status: StatusFail, Detail: err.Error()}
	}
	if a.ReportHash == "" || a.ReportHash != b.ReportHash {
		return Check{Name: "triage-hash", Status: StatusFail, Detail: fmt.Sprintf("hash unstable: %q vs %q", a.ReportHash, b.ReportHash)}
	}
	return Check{Name: "triage-hash", Status: StatusOK, Detail: a.ReportHash}
}

// --- canned, deterministic triage dependencies (production code, not test) ---
//
// These types live in production (non-test) code because checkTriageHash is
// itself production code that must build a real triage report to dogfood
// determinism; the canned deps supply that report's fixed inputs.

type cannedIncidents struct{}

func (cannedIncidents) GetIncident(_ context.Context, id string) (triage.IncidentSummary, error) {
	return triage.IncidentSummary{ID: id, Window: "15m", Confidence: pkgtriage.ConfidenceMedium}, nil
}

type cannedBlast struct{}

func (cannedBlast) BlastSnapshot(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) (triage.BlastSnapshotResult, error) {
	return triage.BlastSnapshotResult{}, nil
}

type cannedStory struct{}

func (cannedStory) FirstFailureStory(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) (triage.FirstFailureResult, error) {
	return triage.FirstFailureResult{}, nil
}

type cannedSignals struct{}

func (cannedSignals) SignalsFor(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) ([]triage.SignalEvidence, error) {
	return nil, nil
}

type cannedNextChecks struct{}

func (cannedNextChecks) NextChecks(_ context.Context, _ triage.IncidentSummary) ([]triage.NextCheckSpec, error) {
	return nil, nil
}
