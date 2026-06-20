package incidents

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestClassifySetsSuspectDeployID(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	ev := testIncidentEvent("e", "t", now, "checkout", "cart.validate", "CHK_500", "")

	// Deployment-store match → SuspectDeployID carries the deployment id.
	got := Classify(ClassificationInput{
		Incident:    base,
		Events:      []*eventv2.Event{ev},
		Deployments: []Deployment{{ID: "dep_1", Service: "checkout", Env: "prod", FirstSeen: now.Add(-time.Minute)}},
	})
	if got.SuspectDeployID != "dep_1" {
		t.Fatalf("SuspectDeployID = %q, want dep_1", got.SuspectDeployID)
	}

	// A deploy *signal* (no deployment-store entry) must NOT set it: suspect_change
	// hydrates provenance from the deployment store, which a signal can't supply.
	got = Classify(ClassificationInput{
		Incident: base,
		Events:   []*eventv2.Event{ev},
		Signals: []signals.Signal{{
			SignalID: "sig_dep", Type: signals.TypeDeploy, Service: "checkout", Env: "prod",
			Timestamp: now.Add(-time.Minute),
		}},
	})
	if got.SuspectDeployID != "" {
		t.Fatalf("deploy signal should not set SuspectDeployID, got %q", got.SuspectDeployID)
	}
}

func TestDeploymentEvidenceSurvivesRuntimeFlood(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}

	// 20 runtime signals far exceed the evidence cap (12).
	var sigs []signals.Signal
	for i := 0; i < 20; i++ {
		sigs = append(sigs, signals.Signal{
			SignalID: "rt_" + strconv.Itoa(i), Type: signals.TypeRuntime, Service: "checkout", Env: "prod",
			Reason: "panic", Severity: signals.SeverityWarning, Timestamp: now.Add(-time.Duration(i) * time.Second),
		})
	}
	got := Classify(ClassificationInput{
		Incident:    base,
		Now:         now,
		Events:      []*eventv2.Event{testIncidentEvent("e", "t", now, "checkout", "cart.validate", "CHK_500", "")},
		Signals:     sigs,
		Deployments: []Deployment{{ID: "dep_flood", Service: "checkout", Env: "prod", FirstSeen: now.Add(-time.Minute)}},
	})

	found := false
	for _, e := range got.Evidence {
		if e.Kind == EvidenceDeployment && e.DeployID == "dep_flood" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deployment evidence evicted by runtime flood (%d evidence rows)", len(got.Evidence))
	}
	if got.SuspectDeployID != "dep_flood" {
		t.Fatalf("SuspectDeployID = %q, want dep_flood", got.SuspectDeployID)
	}
}

type fakeDeploySource struct{ deps []Deployment }

func (f *fakeDeploySource) DeploymentsInWindow(_ context.Context, _, _ time.Time, _ string) ([]Deployment, error) {
	return f.deps, nil
}

// TestEngineSuspectDeployIsSticky proves the correlation persists across ticks:
// once correlated, a later tick that no longer matches a deploy must not drop it.
func TestEngineSuspectDeployIsSticky(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		current: ErrorsResult{Rows: []apiv2.ErrorRow{{
			ErrorFamily: testFamily(), Count: 6, AffectedTraces: 6, SampleTraces: []string{"trace-a"},
		}}},
		blast:  apiv2.BlastRadiusResponse{AffectedRequests: 6, AffectedServices: 2, TopServices: []string{"checkout"}},
		events: []*eventv2.Event{testIncidentEvent("e", "trace-a", now.Add(-time.Minute), "checkout", "payment.charge", "PMT_502", "")},
	}
	deploys := &fakeDeploySource{deps: []Deployment{{ID: "dep_sticky", Service: "checkout", Env: "prod", FirstSeen: now.Add(-2 * time.Minute)}}}
	store := NewMemoryStore()
	engine := NewEngine(reader, nil, deploys, store, Config{MinCount: 5, DeployCorrelationWindow: 15 * time.Minute, SampleLimit: 2}, nil, nil)
	engine.now = func() time.Time { return now }
	if err := engine.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := engine.Active(context.Background())
	if len(rows) != 1 || rows[0].SuspectDeployID != "dep_sticky" {
		t.Fatalf("tick 1: want SuspectDeployID=dep_sticky, got %+v", rows)
	}

	// Tick 2: the deploy source no longer returns the deployment (churn / aged
	// out). The correlation must remain sticky.
	deploys.deps = nil
	now = now.Add(30 * time.Second)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ = engine.Active(context.Background())
	if len(rows) != 1 || rows[0].SuspectDeployID != "dep_sticky" {
		t.Fatalf("tick 2: SuspectDeployID should be sticky, got %+v", rows)
	}
}
