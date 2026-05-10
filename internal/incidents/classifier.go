package incidents

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type ClassificationInput struct {
	Incident    Incident
	Events      []*eventv2.Event
	Signals     []signals.Signal
	Deployments []Deployment
	Now         time.Time
}

type Classification struct {
	Cause                   Cause
	Confidence              Confidence
	Evidence                []Evidence
	NextChecks              []string
	InstrumentationWarnings []string
}

func Classify(input ClassificationInput) Classification {
	evidence := collectTraceEvidence(input.Events)
	evidence = append(evidence, matchingAlertEvidence(input)...)
	warnings := instrumentationWarnings(input.Events, input.Signals)

	if dep := matchingDependencySignal(input); dep != nil {
		evidence = append(evidence, signalEvidence(*dep, "Dependency signal overlaps first failing downstream"))
		return classification(CauseDependency, ConfidenceHigh, evidence, warnings)
	}
	if downstream := firstFailingDownstream(input.Events); downstream != "" {
		evidence = append(evidence, Evidence{
			Kind:       EvidenceTrace,
			Title:      "First failing step calls downstream service",
			Detail:     downstream,
			Service:    downstream,
			OccurredAt: input.Incident.StartedAt,
		})
		return classification(CauseDependency, ConfidenceMedium, evidence, warnings)
	}
	if dep := matchingDeployment(input); dep != nil {
		evidence = append(evidence, deploymentEvidence(*dep))
		return classification(CauseDeploy, ConfidenceHigh, evidence, warnings)
	}
	if sig := matchingSignal(input, signals.TypeDeploy); sig != nil {
		evidence = append(evidence, signalEvidence(*sig, "Deploy signal overlaps incident window"))
		return classification(CauseDeploy, ConfidenceHigh, evidence, warnings)
	}
	if sig := matchingRuntimeSignal(input); sig != nil {
		evidence = append(evidence, signalEvidence(*sig, "Runtime signal overlaps incident window"))
		return classification(CauseRuntime, ConfidenceHigh, evidence, warnings)
	}
	if len(input.Events) > 0 && input.Incident.ErrorFamily.Step != "" && firstFailingDownstream(input.Events) == "" {
		return classification(CauseApp, ConfidenceMedium, evidence, warnings)
	}
	return classification(CauseUnknown, ConfidenceLow, evidence, warnings)
}

func classification(cause Cause, confidence Confidence, evidence []Evidence, warnings []string) Classification {
	return Classification{
		Cause:                   cause,
		Confidence:              confidence,
		Evidence:                normalizeEvidence(evidence, 8),
		NextChecks:              NextChecks(cause, confidence),
		InstrumentationWarnings: uniqueStrings(warnings),
	}
}

func matchingDependencySignal(input ClassificationInput) *signals.Signal {
	downstream := firstFailingDownstream(input.Events)
	for i := range input.Signals {
		sig := input.Signals[i]
		if sig.Type != signals.TypeDependency {
			continue
		}
		if downstream != "" && sig.Service != downstream {
			continue
		}
		return &input.Signals[i]
	}
	return nil
}

func matchingDeployment(input ClassificationInput) *Deployment {
	version := sampleVersion(input.Events)
	for i := range input.Deployments {
		dep := input.Deployments[i]
		if dep.Env != "" && input.Incident.Env != "" && dep.Env != input.Incident.Env {
			continue
		}
		if dep.Service != input.Incident.Service {
			continue
		}
		if version != "" && dep.Version != "" && dep.Version != version {
			continue
		}
		return &input.Deployments[i]
	}
	return nil
}

func matchingRuntimeSignal(input ClassificationInput) *signals.Signal {
	start := input.Incident.StartedAt
	lo := start.Add(-5 * time.Minute)
	hi := start.Add(time.Minute)
	for i := range input.Signals {
		sig := input.Signals[i]
		if sig.Type != signals.TypeRuntime && sig.Type != signals.TypeHealthcheck {
			continue
		}
		if sig.Service != input.Incident.Service {
			continue
		}
		if sig.Timestamp.Before(lo) || sig.Timestamp.After(hi) {
			continue
		}
		return &input.Signals[i]
	}
	return nil
}

func matchingSignal(input ClassificationInput, typ signals.Type) *signals.Signal {
	version := sampleVersion(input.Events)
	for i := range input.Signals {
		sig := input.Signals[i]
		if sig.Type != typ || sig.Service != input.Incident.Service {
			continue
		}
		if version == "" {
			return &input.Signals[i]
		}
		if sigVersion := stringField(sig.Metadata, "version"); sigVersion == "" || sigVersion == version {
			return &input.Signals[i]
		}
	}
	return nil
}

func matchingAlertEvidence(input ClassificationInput) []Evidence {
	start := input.Incident.StartedAt
	lo := start.Add(-15 * time.Minute)
	hi := input.Now
	if hi.IsZero() {
		hi = input.Incident.UpdatedAt
	}
	out := []Evidence{}
	for _, sig := range input.Signals {
		if sig.Type != signals.TypeAlert {
			continue
		}
		if input.Incident.Env != "" && sig.Env != input.Incident.Env {
			continue
		}
		if sig.Service != input.Incident.Service {
			continue
		}
		if sig.Timestamp.Before(lo) || sig.Timestamp.After(hi) {
			continue
		}
		out = append(out, signalEvidence(sig, "External alert overlaps incident window"))
	}
	return out
}

func collectTraceEvidence(events []*eventv2.Event) []Evidence {
	out := make([]Evidence, 0, 2)
	for _, ev := range events {
		if ev == nil || ev.Anchor == nil {
			continue
		}
		out = append(out, Evidence{
			Kind:       EvidenceTrace,
			Title:      "First failing trace sample",
			Detail:     fmt.Sprintf("%s/%s", ev.Anchor.Step, ev.Anchor.ErrorCode),
			Service:    ev.Service,
			TraceID:    ev.TraceID,
			OccurredAt: ev.TsStart,
		})
		break
	}
	return out
}

func deploymentEvidence(dep Deployment) Evidence {
	return Evidence{
		Kind:       EvidenceDeployment,
		Title:      "Deployment overlaps incident window",
		Detail:     dep.Version,
		Service:    dep.Service,
		DeployID:   dep.ID,
		OccurredAt: dep.FirstSeen,
	}
}

func signalEvidence(sig signals.Signal, title string) Evidence {
	fields := map[string]any{
		"type":     string(sig.Type),
		"severity": string(sig.Severity),
		"source":   sig.Source,
	}
	if alertID := stringField(sig.Metadata, "alert_id"); alertID != "" {
		fields["alert_id"] = alertID
	}
	if providerURL := stringField(sig.Metadata, "provider_url"); providerURL != "" {
		fields["provider_url"] = providerURL
	}
	return Evidence{
		Kind:       EvidenceSignal,
		Title:      title,
		Detail:     sig.Reason,
		Service:    sig.Service,
		SignalID:   sig.SignalID,
		OccurredAt: sig.Timestamp,
		Fields:     fields,
	}
}

func normalizeEvidence(evidence []Evidence, limit int) []Evidence {
	sort.SliceStable(evidence, func(i, j int) bool {
		if !evidence[i].OccurredAt.Equal(evidence[j].OccurredAt) {
			return evidence[i].OccurredAt.Before(evidence[j].OccurredAt)
		}
		if evidence[i].Kind != evidence[j].Kind {
			return evidence[i].Kind < evidence[j].Kind
		}
		return evidence[i].Title < evidence[j].Title
	})
	seen := map[string]struct{}{}
	out := make([]Evidence, 0, len(evidence))
	for _, ev := range evidence {
		key := string(ev.Kind) + "|" + ev.Title + "|" + ev.SignalID + "|" + ev.DeployID + "|" + ev.TraceID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ev)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func instrumentationWarnings(events []*eventv2.Event, sigs []signals.Signal) []string {
	var warnings []string
	if sampleVersion(events) == "" {
		warnings = append(warnings, "missing_service_version")
	}
	if firstFailingDownstream(events) != "" && !hasSignalType(sigs, signals.TypeDependency) {
		warnings = append(warnings, "missing_dependency_signal")
	}
	for _, ev := range events {
		if ev != nil && ev.Status == eventv2.StatusPartial {
			warnings = append(warnings, "partial_trace")
			break
		}
	}
	return warnings
}

func firstFailingDownstream(events []*eventv2.Event) string {
	for _, ev := range events {
		if ev == nil || ev.Anchor == nil {
			continue
		}
		for _, step := range ev.Steps {
			if step.Name == ev.Anchor.Step && step.Status == eventv2.StepStatusError && step.Downstream != nil {
				return step.Downstream.Service
			}
		}
	}
	return ""
}

func sampleVersion(events []*eventv2.Event) string {
	for _, ev := range events {
		if ev != nil && ev.Version != "" {
			return ev.Version
		}
	}
	return ""
}

func hasSignalType(sigs []signals.Signal, typ signals.Type) bool {
	for _, sig := range sigs {
		if sig.Type == typ {
			return true
		}
	}
	return false
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
