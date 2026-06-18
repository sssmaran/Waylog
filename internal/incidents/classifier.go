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
	// Runtime signals are correlated evidence, not incident openers (Design
	// Decision 2). Attach every matched runtime signal — infra AND app — as an
	// evidence row regardless of the cause the classifier ultimately picks.
	runtimeSigs := matchingRuntimeSignals(input)
	for i := range runtimeSigs {
		evidence = append(evidence, runtimeEvidence(runtimeSigs[i]))
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

	depSig := matchingDependencySignal(input)
	downstream := firstFailingDownstream(input.Events)
	deployment := matchingDeployment(input)
	var deploySig *signals.Signal
	if deployment == nil {
		deploySig = matchingSignal(input, signals.TypeDeploy)
	}
	hasDeploy := deployment != nil || deploySig != nil

	if depSig != nil {
		ctx.Downstream = depSig.Service
		ctx.DepSignalID = depSig.SignalID
		ctx.DepSignalReason = depSig.Reason
		evidence = append(evidence, signalEvidence(*depSig, dependencyLabel(*depSig)))
	} else if downstream != "" {
		ctx.Downstream = downstream
		evidence = append(evidence, Evidence{
			Kind:       EvidenceTrace,
			Title:      fmt.Sprintf("First failing step calls `%s`", downstream),
			Detail:     downstream,
			Service:    downstream,
			OccurredAt: input.Incident.StartedAt,
		})
	}

	// deployAt anchors the temporal tiebreak; a deploy signal without a
	// timestamp leaves it zero (and therefore loses any tiebreak).
	var deployAt time.Time
	switch {
	case deployment != nil:
		deployAt = deployment.FirstSeen
		ctx.DeployVersion = deployment.Version
		ctx.DeployFirstSeen = deployment.FirstSeen
		evidence = append(evidence, deploymentEvidence(*deployment))
	case deploySig != nil:
		ctx.DeployVersion = stringField(deploySig.Metadata, "version")
		ctx.DeploySignalID = deploySig.SignalID
		if !deploySig.Timestamp.IsZero() {
			ctx.DeployFirstSeen = deploySig.Timestamp
			deployAt = deploySig.Timestamp
		}
		evidence = append(evidence, signalEvidence(*deploySig, deployLabel(*deploySig)))
	}

	switch {
	case depSig != nil && hasDeploy:
		// Both causes are signal-backed: a cause anchored at/before onset beats
		// one that lands after it (a change after the incident started cannot
		// have caused it); among causes on the same side of onset, temporal
		// proximity decides and an exact tie keeps the dependency (ADR 0001).
		// Both evidence rows are already attached; the loser surfaces in next
		// checks.
		if deployBeatsDependency(deployAt, depSig.Timestamp, input.Incident.StartedAt) {
			return classification(CauseDeploy, ConfidenceHigh, evidence, warnings, ctx)
		}
		return classification(CauseDependency, ConfidenceHigh, evidence, warnings, ctx)
	case depSig != nil:
		return classification(CauseDependency, ConfidenceHigh, evidence, warnings, ctx)
	case hasDeploy:
		// A correlated deploy beats the unconfirmed trace-only downstream
		// inference (ADR 0001); the downstream stays as evidence + next check.
		return classification(CauseDeploy, ConfidenceHigh, evidence, warnings, ctx)
	case downstream != "":
		return classification(CauseDependency, ConfidenceMedium, evidence, warnings, ctx)
	}
	if len(runtimeSigs) > 0 {
		top := runtimeSigs[0]
		ctx.RuntimeSignalID = top.SignalID
		ctx.RuntimeReason = top.Reason
		ctx.RuntimeSubtype = stringField(top.Metadata, "subtype")
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

// deployBeatsDependency decides, when both a deploy and a dependency signal
// correlate with the incident, whether the deploy is the better cause. A
// non-zero anchor at/before onset is preferred over one strictly after onset
// (a change that lands after the incident started cannot have caused it); when
// both anchors are on the same side of onset (or either is missing), the one
// closer to onset wins, and an exact tie keeps the dependency.
func deployBeatsDependency(deployAt, depAt, onset time.Time) bool {
	if !deployAt.IsZero() && !depAt.IsZero() {
		deployPrecedes := !deployAt.After(onset)
		depPrecedes := !depAt.After(onset)
		if deployPrecedes != depPrecedes {
			return deployPrecedes
		}
	}
	return closerToOnset(deployAt, depAt, onset)
}

// closerToOnset reports whether a is strictly closer to the incident onset
// than b. A zero time loses: it means "no timestamp", never "at epoch".
func closerToOnset(a, b, onset time.Time) bool {
	return absDuration(onset.Sub(a)) < absDuration(onset.Sub(b))
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
		Evidence:                normalizeEvidence(evidence, 12),
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

// matchingRuntimeSignals returns every runtime/healthcheck signal matching the
// incident's service and env within [StartedAt-15m, Now], sorted by severity
// priority, then timestamp, then signal_id. Returning all matches (not the
// first) lets both infra (oom_killed) and app (panic) evidence coexist.
func matchingRuntimeSignals(input ClassificationInput) []signals.Signal {
	start := input.Incident.StartedAt
	lo := start.Add(-15 * time.Minute)
	hi := input.Now
	if hi.IsZero() {
		hi = input.Incident.UpdatedAt
	}
	if hi.Before(start) {
		hi = start.Add(time.Minute)
	}
	var out []signals.Signal
	for i := range input.Signals {
		if matchRuntimeSignalToIncident(input.Signals[i], input.Incident, lo, hi) {
			out = append(out, input.Signals[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := severityRank(out[i].Severity), severityRank(out[j].Severity); ri != rj {
			return ri > rj
		}
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].SignalID < out[j].SignalID
	})
	return out
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
	if sig.Source != "" {
		return fmt.Sprintf("Runtime %s: %s (%s)", service, reason, sig.Source)
	}
	return fmt.Sprintf("Runtime %s: %s", service, reason)
}

// runtimeEvidence builds a flat EvidenceRuntime row. subtype/source/severity
// live in Fields so the generic Evidence struct stays unchanged while
// acceptance assertions can query subtypes (Design Decision 4).
func runtimeEvidence(sig signals.Signal) Evidence {
	occurred := sig.Timestamp
	if occurred.IsZero() {
		occurred = sig.ReceivedAt
	}
	return Evidence{
		Kind:       EvidenceRuntime,
		Title:      runtimeLabel(sig),
		Detail:     sig.Reason,
		Service:    sig.Service,
		SignalID:   sig.SignalID,
		OccurredAt: occurred,
		Fields: map[string]any{
			"subtype":  stringField(sig.Metadata, "subtype"),
			"source":   sig.Source,
			"severity": string(sig.Severity),
		},
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
	deduped := make([]Evidence, 0, len(evidence))
	for _, ev := range evidence {
		key := string(ev.Kind) + "|" + ev.Title + "|" + ev.SignalID + "|" + ev.DeployID + "|" + ev.TraceID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, ev)
	}
	if limit <= 0 || len(deduped) <= limit {
		return deduped
	}
	// Runtime evidence must never be truncated by the cap: the acceptance gate
	// and the dashboard Runtime panel both rely on every matched runtime signal
	// being present. Keep all EvidenceRuntime rows, then fill remaining slots
	// with the earliest non-runtime rows. Chronological order is preserved
	// because deduped is already sorted and we append in order.
	budget := limit
	for _, ev := range deduped {
		if ev.Kind == EvidenceRuntime {
			budget--
		}
	}
	out := make([]Evidence, 0, limit)
	for _, ev := range deduped {
		if ev.Kind == EvidenceRuntime {
			out = append(out, ev)
			continue
		}
		if budget > 0 {
			out = append(out, ev)
			budget--
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
