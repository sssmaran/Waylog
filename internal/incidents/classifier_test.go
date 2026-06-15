package incidents

import (
	"strings"
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

	t.Run("infra and app runtime both present, deterministic order", func(t *testing.T) {
		infra := signals.Signal{
			SignalID: "sig_oom", Type: signals.TypeRuntime, Service: "checkout", Env: "prod",
			Reason: "OOMKilled", Severity: signals.SeverityCritical, Timestamp: now.Add(-2 * time.Minute),
			Source: "k8s-demo", Metadata: map[string]any{"subtype": "oom_killed"},
		}
		app := signals.Signal{
			SignalID: "sig_panic", Type: signals.TypeRuntime, Service: "checkout", Env: "prod",
			Reason: "runtime panic", Severity: signals.SeverityWarning, Timestamp: now.Add(-time.Minute),
			Source: "go-sdk", Metadata: map[string]any{"subtype": "panic"},
		}
		// Provide app first to prove sort (not input order) drives the result.
		got := Classify(ClassificationInput{
			Incident: base, Now: now,
			Events:  []*eventv2.Event{checkoutEvent},
			Signals: []signals.Signal{app, infra},
		})
		var runtimeRows []Evidence
		for _, ev := range got.Evidence {
			if ev.Kind == EvidenceRuntime {
				runtimeRows = append(runtimeRows, ev)
			}
		}
		if len(runtimeRows) != 2 {
			t.Fatalf("want 2 runtime evidence rows, got %d: %+v", len(runtimeRows), got.Evidence)
		}
		// normalizeEvidence sorts by OccurredAt asc; infra (-2m) precedes app (-1m).
		if runtimeRows[0].SignalID != "sig_oom" || runtimeRows[1].SignalID != "sig_panic" {
			t.Fatalf("runtime evidence order wrong: %+v", runtimeRows)
		}
		if st, _ := runtimeRows[0].Fields["subtype"].(string); st != "oom_killed" {
			t.Fatalf("infra subtype wrong: %+v", runtimeRows[0].Fields)
		}
		if st, _ := runtimeRows[1].Fields["subtype"].(string); st != "panic" {
			t.Fatalf("app subtype wrong: %+v", runtimeRows[1].Fields)
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
		sig.Timestamp = now.Add(-20 * time.Minute)
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

func TestClassifierTemporalTiebreak(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	paymentEvent := testIncidentEvent("e1", "trace-a", now, "checkout", "payment.charge", "PMT_502", "payment")
	depSig := func(ts time.Time) signals.Signal {
		return signals.Signal{
			SignalID: "sig_dep", Type: signals.TypeDependency, Service: "payment", Env: "prod",
			Reason: "upstream_5xx", Severity: signals.SeverityCritical, Timestamp: ts,
		}
	}
	deployAt := func(ts time.Time) Deployment {
		return Deployment{ID: "dep_1", Service: "checkout", Version: "v1", Env: "prod", FirstSeen: ts}
	}
	hasEvidence := func(got Classification, match func(Evidence) bool) bool {
		for _, ev := range got.Evidence {
			if match(ev) {
				return true
			}
		}
		return false
	}
	hasCheck := func(got Classification, substr string) bool {
		for _, line := range got.NextChecks {
			if strings.Contains(line, substr) {
				return true
			}
		}
		return false
	}

	t.Run("deploy closer to onset wins over dependency signal", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{paymentEvent},
			Signals:     []signals.Signal{depSig(now.Add(-10 * time.Minute))},
			Deployments: []Deployment{deployAt(now.Add(-time.Minute))},
		})
		if got.Cause != CauseDeploy || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
		if !hasEvidence(got, func(ev Evidence) bool { return ev.DeployID == "dep_1" }) {
			t.Fatalf("deployment evidence missing: %+v", got.Evidence)
		}
		if !hasEvidence(got, func(ev Evidence) bool { return ev.SignalID == "sig_dep" }) {
			t.Fatalf("losing dependency-signal evidence must still attach: %+v", got.Evidence)
		}
		if !hasCheck(got, "Also verify downstream `payment`") {
			t.Fatalf("next checks must cross-reference the losing dependency cause: %v", got.NextChecks)
		}
	})

	t.Run("dependency signal closer to onset wins over deploy", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{paymentEvent},
			Signals:     []signals.Signal{depSig(now.Add(-time.Minute))},
			Deployments: []Deployment{deployAt(now.Add(-10 * time.Minute))},
		})
		if got.Cause != CauseDependency || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
		if !hasEvidence(got, func(ev Evidence) bool { return ev.DeployID == "dep_1" }) {
			t.Fatalf("losing deployment evidence must still attach: %+v", got.Evidence)
		}
		if !hasCheck(got, "Also verify recent deploy `v1`") {
			t.Fatalf("next checks must cross-reference the losing deploy cause: %v", got.NextChecks)
		}
	})

	t.Run("equal distance keeps dependency priority", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{paymentEvent},
			Signals:     []signals.Signal{depSig(now.Add(-time.Minute))},
			Deployments: []Deployment{deployAt(now.Add(-time.Minute))},
		})
		if got.Cause != CauseDependency || got.Confidence != ConfidenceHigh {
			t.Fatalf("classification=%+v", got)
		}
	})

	t.Run("deploy after onset does not beat a dependency signal before onset", func(t *testing.T) {
		// A routine/rollback deploy that lands during the incident must not be
		// blamed over a dependency signal that preceded onset, even though the
		// deploy's absolute distance to onset is smaller.
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{paymentEvent},
			Signals:     []signals.Signal{depSig(now.Add(-time.Minute))},
			Deployments: []Deployment{deployAt(now.Add(30 * time.Second))},
		})
		if got.Cause != CauseDependency || got.Confidence != ConfidenceHigh {
			t.Fatalf("post-onset deploy must not win over a pre-onset dependency signal: %+v", got)
		}
	})

	t.Run("deploy beats trace-only downstream inference", func(t *testing.T) {
		got := Classify(ClassificationInput{
			Incident:    base,
			Events:      []*eventv2.Event{paymentEvent},
			Deployments: []Deployment{deployAt(now.Add(-time.Minute))},
		})
		if got.Cause != CauseDeploy || got.Confidence != ConfidenceHigh {
			t.Fatalf("trace-only downstream must not beat a correlated deploy: %+v", got)
		}
		if !hasEvidence(got, func(ev Evidence) bool {
			return ev.Kind == EvidenceTrace && strings.Contains(ev.Title, "First failing step calls `payment`")
		}) {
			t.Fatalf("downstream trace evidence must still attach: %+v", got.Evidence)
		}
		if !hasCheck(got, "Also verify downstream `payment`") {
			t.Fatalf("next checks must cross-reference the downstream: %v", got.NextChecks)
		}
	})
}

func TestUnrelatedDependencySignalDoesNotAttachToLeafIncident(t *testing.T) {
	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "db", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	// Leaf-service event (no downstream call) — e.g. DB returning CART_NOT_FOUND.
	leafEvent := testIncidentEvent("e1", "trace-a", now, "db", "cart.lookup", "CART_NOT_FOUND", "")
	// An unrelated dependency signal that happens to be in the signal store for a different service.
	unrelated := signals.Signal{
		SignalID:  "sig_dep_unrelated",
		Type:      signals.TypeDependency,
		Service:   "payment",
		Env:       "prod",
		Reason:    "payment_gateway_5xx",
		Severity:  signals.SeverityCritical,
		Timestamp: now.Add(-time.Minute),
	}

	got := Classify(ClassificationInput{
		Incident: base,
		Events:   []*eventv2.Event{leafEvent},
		Signals:  []signals.Signal{unrelated},
	})

	if got.Cause == CauseDependency {
		t.Fatalf("leaf-service incident with unrelated dep signal should not classify as dependency: %+v", got)
	}
	for _, ev := range got.Evidence {
		if ev.SignalID == "sig_dep_unrelated" {
			t.Fatalf("unrelated dependency signal should not appear in evidence: %+v", ev)
		}
	}
	for _, line := range got.NextChecks {
		if strings.Contains(line, "`payment`") {
			t.Fatalf("next checks should not reference unrelated `payment`: %q", line)
		}
	}
}

func TestNextChecksRuntime(t *testing.T) {
	got := NextChecks(CauseRuntime, ConfidenceHigh, NextCheckContext{
		Service:         "payment",
		RuntimeSignalID: "sig_rt_42",
		RuntimeReason:   "OOMKilled",
		RuntimeSubtype:  "oom_killed",
	})
	if len(got) == 0 {
		t.Fatalf("expected non-empty next checks for runtime cause")
	}
	foundSignalLine := false
	foundMemoryLine := false
	for _, line := range got {
		if strings.Contains(line, "runtime signal") && strings.Contains(line, "`OOMKilled`") && strings.Contains(line, "`payment`") {
			foundSignalLine = true
		}
		if strings.Contains(line, "memory") && strings.Contains(line, "`payment`") {
			foundMemoryLine = true
		}
	}
	if !foundSignalLine {
		t.Fatalf("expected runtime signal line referencing payment+OOMKilled, got %v", got)
	}
	if !foundMemoryLine {
		t.Fatalf("expected memory-usage line for oom_killed subtype, got %v", got)
	}
}

func TestNextChecksRuntimeWithoutSubtypeOmitsCategoryLine(t *testing.T) {
	got := NextChecks(CauseRuntime, ConfidenceHigh, NextCheckContext{
		Service:         "payment",
		RuntimeSignalID: "sig_rt",
		RuntimeReason:   "container restarted",
	})
	for _, line := range got {
		if strings.Contains(line, "memory") || strings.Contains(line, "readiness") || strings.Contains(line, "liveness") {
			t.Fatalf("runtime cause without subtype must not emit memory/probe lines, got %q", line)
		}
	}
}

func TestNextChecksFallbackWhenContextEmpty(t *testing.T) {
	causes := []Cause{CauseDependency, CauseDeploy, CauseRuntime, CauseApp, CauseUnknown}
	for _, cause := range causes {
		got := NextChecks(cause, ConfidenceMedium, NextCheckContext{})
		if len(got) == 0 {
			t.Fatalf("%s: expected non-empty next checks", cause)
		}
		for _, line := range got {
			if strings.Contains(line, "{") || strings.Contains(line, "``") {
				t.Fatalf("%s: empty context produced unfilled placeholder in %q", cause, line)
			}
		}
	}
}

func TestClassifyIncludesAlertEvidenceWithoutChangingCause(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	base := Incident{Service: "checkout", Env: "prod", StartedAt: now, ErrorFamily: testFamily()}
	paymentEvent := testIncidentEvent("e1", "trace-a", now, "checkout", "payment.charge", "PMT_502", "payment")

	got := Classify(ClassificationInput{
		Incident: base,
		Events:   []*eventv2.Event{paymentEvent},
		Signals: []signals.Signal{{
			SignalID:  "sig_alert",
			Type:      signals.TypeAlert,
			Source:    "grafana",
			Service:   "checkout",
			Env:       "prod",
			Severity:  signals.SeverityCritical,
			Reason:    "PMT_502 spike",
			Timestamp: now,
			Metadata:  map[string]any{"alert_id": "alert_1", "provider_url": "https://grafana/alert"},
		}, {
			SignalID:  "sig_other_env",
			Type:      signals.TypeAlert,
			Source:    "grafana",
			Service:   "checkout",
			Env:       "staging",
			Severity:  signals.SeverityCritical,
			Reason:    "staging alert",
			Timestamp: now,
			Metadata:  map[string]any{"alert_id": "alert_staging"},
		}},
		Now: now,
	})
	if got.Cause != CauseDependency {
		t.Fatalf("alert should not override dependency cause: %+v", got)
	}
	for _, ev := range got.Evidence {
		if ev.SignalID == "sig_other_env" {
			t.Fatalf("alert evidence from another env should not be included: %+v", ev)
		}
		if ev.SignalID == "sig_alert" {
			wantTitle := "critical: PMT_502 spike (grafana)"
			if ev.Title != wantTitle {
				t.Fatalf("alert evidence title %q, want %q", ev.Title, wantTitle)
			}
			if ev.Fields["alert_id"] != "alert_1" {
				t.Fatalf("alert metadata missing: %+v", ev.Fields)
			}
			return
		}
	}
	t.Fatalf("alert evidence missing: %+v", got.Evidence)
}

func TestNormalizeEvidence_RuntimeSurvivesCap(t *testing.T) {
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	var ev []Evidence
	// 20 earlier alert rows, all occurring before the runtime events.
	for i := 0; i < 20; i++ {
		ev = append(ev, Evidence{
			Kind: EvidenceSignal, Title: "alert", SignalID: "a" + string(rune('A'+i)),
			OccurredAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	// Two runtime rows occurring AFTER all the alerts — would be truncated by a
	// naive time-sorted cap.
	ev = append(ev,
		Evidence{Kind: EvidenceRuntime, Title: "Runtime checkout: OOMKilled", SignalID: "oom",
			OccurredAt: base.Add(time.Hour), Fields: map[string]any{"subtype": "oom_killed"}},
		Evidence{Kind: EvidenceRuntime, Title: "Runtime checkout: panic", SignalID: "panic",
			OccurredAt: base.Add(2 * time.Hour), Fields: map[string]any{"subtype": "panic"}},
	)
	out := normalizeEvidence(ev, 12)
	subtypes := map[string]bool{}
	for _, e := range out {
		if e.Kind == EvidenceRuntime {
			st, _ := e.Fields["subtype"].(string)
			subtypes[st] = true
		}
	}
	if !subtypes["oom_killed"] || !subtypes["panic"] {
		t.Fatalf("runtime rows truncated by cap; out kinds=%v", out)
	}
	if len(out) > 12 {
		t.Fatalf("cap exceeded: %d rows", len(out))
	}
}
