package incidents

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestClassifierRules(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	paymentEvent := testIncidentEvent("e1", "trace-a", now, "checkout", "payment.charge", "PMT_502", "payment")

	t.Run("dependency with signal", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{paymentEvent},
			Signals: []signals.Signal{{
				SignalID:  "sig_dep",
				Type:      signals.TypeDependency,
				Service:   "payment",
				Env:       "prod",
				Reason:    "upstream_5xx",
				Severity:  signals.SeverityCritical,
				Timestamp: now.Add(-time.Minute),
			}},
		})
		if got.Cause != CauseDependency || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("dependency trace only", func(t *testing.T) {
		got := Classify(ClassificationInput{Incident: base, Events: []*eventv2.Event{paymentEvent}})
		if got.Cause != CauseDependency || got.Confidence != ConfidenceMedium {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("deploy", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{testIncidentEvent("e2", "trace-b", now, "checkout", "cart.validate", "CHK_500", "")},
			Deployments: []Deployment{{ID: "dep_1", Service: "checkout", Version: "v1", Env: "prod", FirstSeen: now.Add(-time.Minute)}},
		})
		if got.Cause != CauseDeploy || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("app", func(t *testing.T) {
		got := Classify(ClassificationInput{Incident: base, Events: []*eventv2.Event{testIncidentEvent("e3", "trace-c", now, "checkout", "cart.validate", "CHK_500", "")}})
		if got.Cause != CauseApp || got.Confidence != ConfidenceMedium {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		got := Classify(ClassificationInput{Incident: base})
		if got.Cause != CauseUnknown || got.Confidence != ConfidenceLow {
			t.Fatalf("classification=%+v", got)
		}
	})
}
