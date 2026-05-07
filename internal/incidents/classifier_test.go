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

func TestClassifierRuntime(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	checkoutEvent := testIncidentEvent("e1", "trace-a", now, "checkout", "cart.validate", "CHK_500", "")
	paymentEvent := testIncidentEvent("e2", "trace-b", now, "checkout", "payment.charge", "PMT_502", "payment")

	runtimeSig := signals.Signal{
		SignalID:  "sig_rt",
		Type:      signals.TypeRuntime,
		Service:   "checkout",
		Env:       "prod",
		Reason:    "container restarted",
		Severity:  signals.SeverityWarning,
		Timestamp: now.Add(-time.Minute),
	}

	t.Run("runtime signal in window", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{runtimeSig},
		})
		if got.Cause != CauseRuntime || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
		found := false
		for _, ev := range got.Evidence {
			if ev.SignalID == "sig_rt" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("runtime signal evidence missing: %+v", got.Evidence)
		}
	})

	t.Run("healthcheck signal in window", func(t *testing.T) {
		sig := runtimeSig
		sig.SignalID = "sig_hc"
		sig.Type = signals.TypeHealthcheck
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{sig},
		})
		if got.Cause != CauseRuntime || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("alert with OOM reason does not classify runtime", func(t *testing.T) {
		sig := runtimeSig
		sig.Type = signals.TypeAlert
		sig.Reason = "OOM kill"
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{sig},
		})
		if got.Cause == CauseRuntime {
			t.Fatalf("alert signal classified as runtime: %+v", got)
		}
	})

	t.Run("runtime signal outside window", func(t *testing.T) {
		sig := runtimeSig
		sig.Timestamp = now.Add(-6 * time.Minute)
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{sig},
		})
		if got.Cause == CauseRuntime {
			t.Fatalf("out-of-window signal classified as runtime: %+v", got)
		}
	})

	t.Run("runtime signal for different service", func(t *testing.T) {
		sig := runtimeSig
		sig.Service = "payment"
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{sig},
		})
		if got.Cause == CauseRuntime {
			t.Fatalf("foreign-service signal classified as runtime: %+v", got)
		}
	})

	t.Run("deploy beats runtime", func(t *testing.T) {
		deploySig := signals.Signal{
			SignalID:  "sig_dep",
			Type:      signals.TypeDeploy,
			Service:   "checkout",
			Env:       "prod",
			Severity:  signals.SeverityWarning,
			Timestamp: now.Add(-time.Minute),
		}
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{checkoutEvent},
			Signals:  []signals.Signal{runtimeSig, deploySig},
		})
		if got.Cause != CauseDeploy {
			t.Fatalf("expected deploy, got %+v", got)
		}
	})

	t.Run("dependency beats runtime", func(t *testing.T) {
		depSig := signals.Signal{
			SignalID:  "sig_depy",
			Type:      signals.TypeDependency,
			Service:   "payment",
			Env:       "prod",
			Reason:    "upstream_5xx",
			Severity:  signals.SeverityCritical,
			Timestamp: now.Add(-time.Minute),
		}
		got := Classify(ClassificationInput{
			Incident: base,
			Events:   []*eventv2.Event{paymentEvent},
			Signals:  []signals.Signal{runtimeSig, depSig},
		})
		if got.Cause != CauseDependency {
			t.Fatalf("expected dependency, got %+v", got)
		}
	})
}

func TestNextChecksRuntime(t *testing.T) {
	got := NextChecks(CauseRuntime, ConfidenceHigh)
	if len(got) == 0 {
		t.Fatalf("expected non-empty next checks for runtime cause")
	}
}
