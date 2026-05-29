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
	alerts := matchingAlerts(input)
	for _, sig := range alerts {
		evidence = append(evidence, signalEvidence(sig, alertLabel(sig)))
	}
	warnings := instrumentationWarnings(input.Events, input.Signals)
	ctx := NextCheckContext{
		Service:                 input.Incident.Service,
		ErrorCode:               input.Incident.ErrorFamily.ErrorCode,
		Step:                    input.Incident.ErrorFamily.Step,
		SampleTraceID:           sampleTraceID(input),
		MissingServiceVersion:   containsString(warnings, "missing_service_version"),
		MissingDependencySignal: containsString(warnings, "missing_dependency_signal"),
		HasPartialTrace:         containsString(warnings, "partial_trace"),
	}
	if top := pickTopAlert(alerts); top != nil {
		ctx.AlertSignalID = top.SignalID
		ctx.AlertID = stringField(top.Metadata, "alert_id")
		ctx.AlertSource = top.Source
		ctx.AlertProviderURL = stringField(top.Metadata, "provider_url")
	}

	if dep := matchingDependencySignal(input); dep != nil {
		ctx.Downstream = dep.Service
		ctx.DepSignalID = dep.SignalID
		ctx.DepSignalReason = dep.Reason
		evidence = append(evidence, signalEvidence(*dep, dependencyLabel(*dep)))
		return classification(CauseDependency, ConfidenceHigh, evidence, warnings, ctx)
	}
	if downstream := firstFailingDownstream(input.Events); downstream != "" {
		ctx.Downstream = downstream
		evidence = append(evidence, Evidence{
			Kind:       EvidenceTrace,
			Title:      fmt.Sprintf("First failing step calls `%s`", downstream),
			Detail:     downstream,
			Service:    downstream,
			OccurredAt: input.Incident.StartedAt,
		})
		return classification(CauseDependency, ConfidenceMedium, evidence, warnings, ctx)
	}
	if dep := matchingDeployment(input); dep != nil {
		ctx.DeployVersion = dep.Version
		ctx.DeployFirstSeen = dep.FirstSeen
		evidence = append(evidence, deploymentEvidence(*dep))
		return classification(CauseDeploy, ConfidenceHigh, evidence, warnings, ctx)
	}
	if sig := matchingSignal(input, signals.TypeDeploy); sig != nil {
		ctx.DeployVersion = stringField(sig.Metadata, "version")
		ctx.DeploySignalID = sig.SignalID
		if !sig.Timestamp.IsZero() {
			ctx.DeployFirstSeen = sig.Timestamp
		}
		evidence = append(evidence, signalEvidence(*sig, deployLabel(*sig)))
		return classification(CauseDeploy, ConfidenceHigh, evidence, warnings, ctx)
	}
	if sig := matchingRuntimeSignal(input); sig != nil {
		ctx.RuntimeSignalID = sig.SignalID
		ctx.RuntimeReason = sig.Reason
		ctx.RuntimeSubtype = stringField(sig.Metadata, "subtype")
		evidence = append(evidence, signalEvidence(*sig, runtimeLabel(*sig)))
		return classification(CauseRuntime, ConfidenceHigh, evidence, warnings, ctx)
	}
	if len(input.Events) > 0 && input.Incident.ErrorFamily.Step != "" && firstFailingDownstream(input.Events) == "" {
		return classification(CauseApp, ConfidenceMedium, evidence, warnings, ctx)
	}
	return classification(CauseUnknown, ConfidenceLow, evidence, warnings, ctx)
}

func sampleTraceID(input ClassificationInput) string {
	if len(input.Incident.SampleTraces) > 0 && input.Incident.SampleTraces[0] != "" {
		return input.Incident.SampleTraces[0]
	}
	for _, ev := range input.Events {
		if ev != nil && ev.TraceID != "" {
			return ev.TraceID
		}
	}
	return ""
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func classification(cause Cause, confidence Confidence, evidence []Evidence, warnings []string, ctx NextCheckContext) Classification {
	return Classification{
		Cause:                   cause,
		Confidence:              confidence,
		Evidence:                normalizeEvidence(evidence, 8),
		NextChecks:              NextChecks(cause, confidence, ctx),
		InstrumentationWarnings: uniqueStrings(warnings),
	}
}

func matchingDependencySignal(input ClassificationInput) *signals.Signal {
	downstream := firstFailingDownstream(input.Events)
	if downstream == "" {
		return nil
	}
	for i := range input.Signals {
		sig := input.Signals[i]
		if sig.Type != signals.TypeDependency {
			continue
		}
		if sig.Service != downstream {
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

func matchingAlerts(input ClassificationInput) []signals.Signal {
	start := input.Incident.StartedAt
	lo := start.Add(-15 * time.Minute)
	hi := input.Now
	if hi.IsZero() {
		hi = input.Incident.UpdatedAt
	}
	var out []signals.Signal
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
		out = append(out, sig)
	}
	return out
}

func pickTopAlert(alerts []signals.Signal) *signals.Signal {
	if len(alerts) == 0 {
		return nil
	}
	best := &alerts[0]
	bestRank := severityRank(best.Severity)
	for i := 1; i < len(alerts); i++ {
		cand := &alerts[i]
		rank := severityRank(cand.Severity)
		if rank > bestRank || (rank == bestRank && cand.Timestamp.After(best.Timestamp)) {
			best = cand
			bestRank = rank
		}
	}
	return best
}

func severityRank(s signals.Severity) int {
	switch s {
	case signals.SeverityCritical:
		return 3
	case signals.SeverityWarning:
		return 2
	case signals.SeverityInfo:
		return 1
	}
	return 0
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
		Title:      deploymentLabel(dep),
		Detail:     dep.Version,
		Service:    dep.Service,
		DeployID:   dep.ID,
		OccurredAt: dep.FirstSeen,
	}
}

func alertLabel(sig signals.Signal) string {
	sev := string(sig.Severity)
	if sev == "" {
		sev = "info"
	}
	reason := sig.Reason
	if reason == "" {
		reason = "external alert"
	}
	source := sig.Source
	if source == "" {
		source = "alert"
	}
	return fmt.Sprintf("%s: %s (%s)", sev, reason, source)
}

func dependencyLabel(sig signals.Signal) string {
	service := sig.Service
	if service == "" {
		service = "downstream"
	}
	reason := sig.Reason
	if reason == "" {
		reason = "dependency signal"
	}
	return fmt.Sprintf("Dependency %s: %s", service, reason)
}

func deployLabel(sig signals.Signal) string {
	service := sig.Service
	if service == "" {
		service = "service"
	}
	detail := stringField(sig.Metadata, "version")
	if detail == "" {
		detail = sig.Reason
	}
	if detail == "" {
		detail = "deploy event"
	}
	return fmt.Sprintf("Deploy %s: %s", service, detail)
}

func deploymentLabel(dep Deployment) string {
	service := dep.Service
	if service == "" {
		service = "service"
	}
	version := dep.Version
	if version == "" {
		version = "new revision"
	}
	return fmt.Sprintf("Deploy %s: %s", service, version)
}

func runtimeLabel(sig signals.Signal) string {
	service := sig.Service
	if service == "" {
		service = "service"
	}
	reason := sig.Reason
	if reason == "" {
		reason = "runtime event"
	}
	return fmt.Sprintf("Runtime %s: %s", service, reason)
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
