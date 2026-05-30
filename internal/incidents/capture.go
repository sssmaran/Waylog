package incidents

import (
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

// pickAnchorTsStart returns the wall-clock TsStart of the failed event in
// events whose Anchor.Step (and ideally Anchor.ErrorCode) match family.
// Two-step matching, earliest wins:
//  1. Anchor.Step == family.Step AND Anchor.ErrorCode == family.ErrorCode
//  2. Anchor.Step == family.Step (ignoring ErrorCode)
//  3. ok=false
func pickAnchorTsStart(events []*eventv2.Event, family apiv2.ErrorFamily) (time.Time, bool) {
	var bestStrict, bestLoose time.Time
	var hasStrict, hasLoose bool
	for _, ev := range events {
		if ev == nil || ev.Anchor == nil {
			continue
		}
		if ev.Anchor.Step != family.Step {
			continue
		}
		if !hasLoose || ev.TsStart.Before(bestLoose) {
			bestLoose, hasLoose = ev.TsStart, true
		}
		if ev.Anchor.ErrorCode == family.ErrorCode {
			if !hasStrict || ev.TsStart.Before(bestStrict) {
				bestStrict, hasStrict = ev.TsStart, true
			}
		}
	}
	if hasStrict {
		return bestStrict, true
	}
	if hasLoose {
		return bestLoose, true
	}
	return time.Time{}, false
}

// newBlastEvidence projects an apiv2.BlastRadiusResponse into a BlastEvidence
// snapshot. Caller provides status: pass CaptureOK on a successful read
// (zero counts are a valid OK), CaptureMissing if the reader call faulted
// (panic-recovered upstream) — in which case b is the zero-value response
// and we record zeros with status=missing rather than misreporting OK.
func newBlastEvidence(b apiv2.BlastRadiusResponse, capturedAt time.Time, status EvidenceCaptureStatus) *BlastEvidence {
	return &BlastEvidence{
		AffectedRequests: b.AffectedRequests,
		AffectedUsers:    b.AffectedUsers,
		AffectedServices: b.AffectedServices,
		TopServices:      append([]string(nil), b.TopServices...),
		SampledTraces:    append([]string(nil), b.SampleTraces...),
		CapturedAt:       capturedAt,
		CaptureStatus:    status,
	}
}

// newPropagationEvidence projects a StoryResponse (possibly nil) into a
// PropagationEvidence snapshot. status is one of CaptureOK / CapturePartial /
// CaptureMissing per the spec's mapping; firstSeenAt is the wall-clock
// anchor TsStart (nil if unavailable).
func newPropagationEvidence(story *apiv2.StoryResponse, sampleTraceID string, firstSeenAt *time.Time, capturedAt time.Time) *PropagationEvidence {
	if story == nil {
		return &PropagationEvidence{
			SampleTraceID: sampleTraceID,
			CapturedAt:    capturedAt,
			CaptureStatus: CaptureMissing,
		}
	}
	status := CaptureOK
	originStep := ""
	if story.Anchor != nil {
		originStep = story.Anchor.Step
	} else {
		status = CapturePartial
	}
	if len(story.Path) == 0 {
		status = CapturePartial
	}
	if firstSeenAt == nil {
		status = CapturePartial
	}
	path := make([]PropagationStep, 0, len(story.Path))
	for _, s := range story.Path {
		path = append(path, PropagationStep{
			Service:    story.Service,
			Step:       s.Name,
			StartMS:    s.StartMS,
			DurationMS: s.DurationMS,
			Status:     s.Status,
			ErrorCode:  s.ErrorCode,
		})
	}
	return &PropagationEvidence{
		OriginService: story.Service,
		OriginStep:    originStep,
		Path:          path,
		SampleTraceID: sampleTraceID,
		FirstSeenAt:   firstSeenAt,
		CapturedAt:    capturedAt,
		CaptureStatus: status,
	}
}

// updatePropagationSnapshot applies the Opening/Latest lifecycle to
// PropagationSnapshot. Opening is set only on the first OK capture (or
// preserved if already set). Latest is always overwritten with the new
// attempt, including partial/missing.
func updatePropagationSnapshot(prior *PropagationSnapshot, fresh *PropagationEvidence) *PropagationSnapshot {
	if prior == nil {
		prior = &PropagationSnapshot{}
	}
	out := &PropagationSnapshot{Opening: prior.Opening, Latest: fresh}
	if out.Opening == nil && fresh != nil && fresh.CaptureStatus == CaptureOK {
		out.Opening = fresh
	}
	return out
}

// updateBlastSnapshot applies the Opening/Latest lifecycle to BlastSnapshot.
// Symmetric to updatePropagationSnapshot; independence rule means this
// runs even if propagation capture failed.
func updateBlastSnapshot(prior *BlastSnapshot, fresh *BlastEvidence) *BlastSnapshot {
	if prior == nil {
		prior = &BlastSnapshot{}
	}
	out := &BlastSnapshot{Opening: prior.Opening, Latest: fresh}
	if out.Opening == nil && fresh != nil && fresh.CaptureStatus == CaptureOK {
		out.Opening = fresh
	}
	return out
}

func captureAlertEvidenceFromSignals(rows []signals.Signal, inc Incident, capturedAt time.Time, matchWindow time.Duration) *AlertEvidence {
	if matchWindow <= 0 {
		matchWindow = 15 * time.Minute
	}
	if matchWindow > 24*time.Hour {
		matchWindow = 24 * time.Hour
	}
	matches := make([]MatchedAlert, 0, len(rows))
	for i := range rows {
		sig := rows[i]
		if ok, strategy := matchAlertSignalToIncident(&sig, inc, matchWindow, capturedAt); ok {
			matches = append(matches, matchedAlertFromSignal(sig, strategy))
		}
	}
	status := CaptureMissing
	if len(matches) > 0 {
		status = CaptureOK
	}
	return &AlertEvidence{Matches: matches, CapturedAt: capturedAt, CaptureStatus: status}
}

func matchAlertSignalToIncident(sig *signals.Signal, inc Incident, matchWindow time.Duration, now time.Time) (bool, string) {
	if sig == nil || sig.Type != signals.TypeAlert {
		return false, ""
	}
	ts := sig.Timestamp
	if ts.IsZero() {
		ts = sig.ReceivedAt
	}
	if !ts.IsZero() {
		lo := inc.StartedAt.Add(-matchWindow)
		hi := now
		if ts.Before(lo) || ts.After(hi) {
			return false, ""
		}
	}
	if id := signalMetaString(sig.Metadata, "incident_id"); id != "" {
		if id == inc.IncidentID {
			return true, "incident_id"
		}
		return false, ""
	}
	if sig.Env != "" && inc.Env != "" && sig.Env != inc.Env {
		return false, ""
	}
	if sig.Service != "" && sig.Service != inc.ErrorFamily.Service {
		return false, ""
	}
	if code := signalMetaString(sig.Metadata, "error_code"); code != "" && code == inc.ErrorFamily.ErrorCode {
		step := signalMetaString(sig.Metadata, "step")
		if step == "" || step == inc.ErrorFamily.Step {
			return true, "family"
		}
	}
	return false, ""
}

func matchedAlertFromSignal(sig signals.Signal, strategy string) MatchedAlert {
	evidenceIDs := []string(nil)
	if sig.SignalID != "" {
		evidenceIDs = []string{sig.SignalID}
	}
	matchedAt := sig.Timestamp
	if matchedAt.IsZero() {
		matchedAt = sig.ReceivedAt
	}
	return MatchedAlert{
		SignalID:    sig.SignalID,
		AlertID:     signalMetaString(sig.Metadata, "alert_id"),
		Source:      sig.Source,
		Severity:    string(sig.Severity),
		Reason:      sig.Reason,
		ProviderURL: signalMetaString(sig.Metadata, "provider_url"),
		EvidenceIDs: evidenceIDs,
		MatchedAt:   matchedAt,
		Strategy:    strategy,
	}
}

func signalMetaString(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func updateAlertSnapshot(prior *AlertSnapshot, fresh *AlertEvidence) *AlertSnapshot {
	if prior == nil {
		prior = &AlertSnapshot{}
	}
	out := &AlertSnapshot{Opening: prior.Opening, Latest: fresh}
	if out.Opening == nil && fresh != nil && fresh.CaptureStatus == CaptureOK {
		out.Opening = fresh
	}
	return out
}

// matchRuntimeSignalToIncident reports whether sig is a runtime/healthcheck
// signal for inc's service within [lo, hi]. Env mismatch excludes (signals are
// already env-filtered at query time; this is a defensive second guard — see
// Critical Design Decision 1: runtime signals must carry env to correlate).
func matchRuntimeSignalToIncident(sig signals.Signal, inc Incident, lo, hi time.Time) bool {
	if sig.Type != signals.TypeRuntime && sig.Type != signals.TypeHealthcheck {
		return false
	}
	if sig.Env != "" && inc.Env != "" && sig.Env != inc.Env {
		return false
	}
	if sig.Service != inc.Service {
		return false
	}
	ts := sig.Timestamp
	if ts.IsZero() {
		ts = sig.ReceivedAt
	}
	if ts.Before(lo) || ts.After(hi) {
		return false
	}
	return true
}

func runtimeSeverityRank(s string) int {
	switch signals.Severity(s) {
	case signals.SeverityCritical:
		return 3
	case signals.SeverityWarning:
		return 2
	case signals.SeverityInfo:
		return 1
	}
	return 0
}

// sortRuntimeMatches orders runtime evidence deterministically: severity
// priority (critical > warning > info), then OccurredAt ascending, then
// SignalID. Stable ordering keeps report_hash stable across ticks.
func sortRuntimeMatches(m []RuntimeEvidence) {
	sort.SliceStable(m, func(i, j int) bool {
		if ri, rj := runtimeSeverityRank(m[i].Severity), runtimeSeverityRank(m[j].Severity); ri != rj {
			return ri > rj
		}
		if !m[i].OccurredAt.Equal(m[j].OccurredAt) {
			return m[i].OccurredAt.Before(m[j].OccurredAt)
		}
		return m[i].SignalID < m[j].SignalID
	})
}

func runtimeEvidenceFromSignal(sig signals.Signal, capturedAt time.Time) RuntimeEvidence {
	occurred := sig.Timestamp
	if occurred.IsZero() {
		occurred = sig.ReceivedAt
	}
	var meta map[string]any
	if len(sig.Metadata) > 0 {
		meta = make(map[string]any, len(sig.Metadata))
		for k, v := range sig.Metadata {
			meta[k] = v
		}
	}
	return RuntimeEvidence{
		Subtype:       signalMetaString(sig.Metadata, "subtype"),
		Service:       sig.Service,
		Reason:        sig.Reason,
		Severity:      string(sig.Severity),
		Source:        sig.Source,
		SignalID:      sig.SignalID,
		OccurredAt:    occurred,
		Metadata:      meta,
		CapturedAt:    capturedAt,
		CaptureStatus: CaptureOK,
	}
}

// captureRuntimeEvidence projects all runtime signals matching inc within the
// window into sorted RuntimeEvidence rows. Window mirrors alert capture:
// [StartedAt-matchWindow, capturedAt]. The engine passes DeployCorrelationWindow
// here, which matches the classifier's 15m runtime lookback at the default
// config so the snapshot and the flat evidence rows agree on what matched.
func captureRuntimeEvidence(rows []signals.Signal, inc Incident, capturedAt time.Time, matchWindow time.Duration) []RuntimeEvidence {
	if matchWindow <= 0 {
		matchWindow = 15 * time.Minute
	}
	if matchWindow > 24*time.Hour {
		matchWindow = 24 * time.Hour
	}
	lo := inc.StartedAt.Add(-matchWindow)
	out := make([]RuntimeEvidence, 0)
	for i := range rows {
		if matchRuntimeSignalToIncident(rows[i], inc, lo, capturedAt) {
			out = append(out, runtimeEvidenceFromSignal(rows[i], capturedAt))
		}
	}
	sortRuntimeMatches(out)
	return out
}

// updateRuntimeSnapshot sets Matches to exactly the fresh windowed capture
// (already sorted) so the live surfaces (API/dashboard/report) only ever show
// currently-correlating runtime signals — never stale ones that have aged out
// of the query window. Opening (earliest ever) and Latest (most recent ever)
// are preserved across ticks as historical provenance. A tick with zero matches
// clears Matches but keeps that provenance, mirroring how alert evidence sets
// its Latest to the fresh (possibly empty) capture each tick.
func updateRuntimeSnapshot(prior *RuntimeSnapshot, fresh []RuntimeEvidence) *RuntimeSnapshot {
	if len(fresh) == 0 {
		if prior == nil {
			return nil
		}
		return &RuntimeSnapshot{Opening: prior.Opening, Latest: prior.Latest}
	}
	out := &RuntimeSnapshot{Matches: fresh}
	if prior != nil {
		out.Opening = prior.Opening
		out.Latest = prior.Latest
	}
	for i := range fresh {
		if out.Opening == nil || fresh[i].OccurredAt.Before(out.Opening.OccurredAt) {
			cp := fresh[i]
			out.Opening = &cp
		}
		if out.Latest == nil || fresh[i].OccurredAt.After(out.Latest.OccurredAt) {
			cp := fresh[i]
			out.Latest = &cp
		}
	}
	return out
}
