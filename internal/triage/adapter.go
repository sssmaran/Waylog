package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

// Upstream collaborator interfaces. Defined narrowly so adapters are testable
// without instantiating real engines/stores. Production wiring (Task 11)
// satisfies these with *incidents.Engine (Get / BlastRadius+Errors), the
// signal store, and a closure over (*core.Graph, *tracestore.Store).

// IncidentReader returns a single incident by ID. *incidents.Engine satisfies
// this via its Get method.
type IncidentReader interface {
	Get(ctx context.Context, id string) (incidents.Incident, error)
}

// BlastReader exposes the read-side queries the blast adapter needs. The
// production reader passed to *incidents.Engine (incidents.Reader) satisfies
// this directly because the method signatures are identical.
type BlastReader interface {
	BlastRadius(f incidents.SearchFilter, key apiv2.BlastKey) apiv2.BlastRadiusResponse
	Errors(f incidents.SearchFilter, limit int) incidents.ErrorsResult
}

// SignalStore is the read surface of internal/signals.Store the adapter calls.
type SignalStore interface {
	Query(ctx context.Context, f signals.Filter) ([]signals.Signal, error)
}

// StoryBuildFunc renders the public-shape trace story for a given trace ID.
// Production wiring closes over *ingestv2.Reader and calls
// Reader.TraceStoryByTraceID. Tests inject a stub directly. The bool return
// is the "found" indicator: when false, the adapter returns an empty result
// without erroring.
type StoryBuildFunc func(traceID string) (apiv2.StoryResponse, bool)

// ----- adapter implementations -----

const defaultWindowLabel = "15m"

type incidentLookupAdapter struct{ r IncidentReader }

func NewIncidentLookupAdapter(r IncidentReader) IncidentLookup {
	return incidentLookupAdapter{r: r}
}

func (a incidentLookupAdapter) GetIncident(ctx context.Context, id string) (IncidentSummary, error) {
	inc, err := a.r.Get(ctx, id)
	if err != nil {
		if errors.Is(err, incidents.ErrNotFound) {
			return IncidentSummary{}, fmt.Errorf("%w: %s", ErrUnknownIncident, id)
		}
		return IncidentSummary{}, err
	}
	return IncidentSummary{
		ID:          inc.IncidentID,
		Window:      defaultWindowLabel,
		Env:         inc.Env,
		StartedAt:   inc.StartedAt,
		UpdatedAt:   inc.UpdatedAt,
		Service:     inc.ErrorFamily.Service,
		Step:        inc.ErrorFamily.Step,
		ErrorCode:   inc.ErrorFamily.ErrorCode,
		Confidence:  mapConfidence(inc.Confidence),
		NextChecks:  append([]string(nil), inc.NextChecks...),
		Propagation: inc.Propagation,
		Blast:       inc.Blast,
	}, nil
}

// mapConfidence converts an incidents.Confidence string to its pkg/triage
// counterpart. Unknown values default to medium so the produced report
// always passes Validate.
func mapConfidence(c incidents.Confidence) pkgtriage.Confidence {
	switch c {
	case incidents.ConfidenceHigh:
		return pkgtriage.ConfidenceHigh
	case incidents.ConfidenceLow:
		return pkgtriage.ConfidenceLow
	case incidents.ConfidenceMedium:
		return pkgtriage.ConfidenceMedium
	default:
		return pkgtriage.ConfidenceMedium
	}
}

type blastQueryAdapter struct{ r BlastReader }

func NewBlastQueryAdapter(r BlastReader) BlastQuery {
	return blastQueryAdapter{r: r}
}

func (a blastQueryAdapter) BlastSnapshot(ctx context.Context, inc IncidentSummary, opts BuildOptions) (BlastSnapshotResult, error) {
	if inc.Blast != nil && inc.Blast.Latest != nil {
		bl := inc.Blast.Latest
		users := 0
		if bl.AffectedUsers != nil {
			users = *bl.AffectedUsers
		}
		families, err := a.topErrorFamilies(inc, opts)
		if err != nil {
			return BlastSnapshotResult{}, err
		}
		return BlastSnapshotResult{
			Requests:         bl.AffectedRequests,
			Users:            users,
			Services:         bl.AffectedServices,
			TopErrorFamilies: families,
		}, nil
	}
	return a.blastSnapshotFromReader(ctx, inc, opts)
}

// blastSnapshotFromReader is the pre-v1.0 computation path. Called when the
// incident has no Blast.Latest snapshot (legacy stored incidents, or a tick
// where capture failed and Latest is missing entirely).
func (a blastQueryAdapter) blastSnapshotFromReader(_ context.Context, inc IncidentSummary, opts BuildOptions) (BlastSnapshotResult, error) {
	filter := blastFilter(inc, opts)
	br := a.r.BlastRadius(filter, apiv2.BlastKey{
		Service:   inc.Service,
		Step:      inc.Step,
		ErrorCode: inc.ErrorCode,
	})
	users := 0
	if br.AffectedUsers != nil {
		users = *br.AffectedUsers
	}
	families, err := a.topErrorFamiliesWithFilter(filter)
	if err != nil {
		return BlastSnapshotResult{}, err
	}
	return BlastSnapshotResult{
		Requests:         br.AffectedRequests,
		Users:            users,
		Services:         br.AffectedServices,
		TopErrorFamilies: families,
	}, nil
}

func (a blastQueryAdapter) topErrorFamilies(inc IncidentSummary, opts BuildOptions) ([]pkgtriage.ErrorFamily, error) {
	return a.topErrorFamiliesWithFilter(blastFilter(inc, opts))
}

func (a blastQueryAdapter) topErrorFamiliesWithFilter(filter incidents.SearchFilter) ([]pkgtriage.ErrorFamily, error) {
	rows := a.r.Errors(filter, 5).Rows
	families := make([]pkgtriage.ErrorFamily, 0, len(rows))
	for _, row := range rows {
		families = append(families, pkgtriage.ErrorFamily{
			Service:   row.ErrorFamily.Service,
			Step:      row.ErrorFamily.Step,
			ErrorCode: row.ErrorFamily.ErrorCode,
			Count:     row.Count,
		})
	}
	return families, nil
}

func blastFilter(inc IncidentSummary, opts BuildOptions) incidents.SearchFilter {
	end := opts.Now
	if end.IsZero() {
		end = inc.UpdatedAt
	}
	window := opts.Window
	if window <= 0 {
		window = defaultWindow
	}
	return incidents.SearchFilter{
		Service:   inc.Service,
		ErrorCode: inc.ErrorCode,
		Since:     end.Add(-window),
		Until:     end,
	}
}

type storyBuilderAdapter struct {
	r     IncidentReader
	build StoryBuildFunc
}

// NewStoryBuilderAdapter wraps an upstream incident reader (to discover the
// first-failure trace ID) and a story-build function (production: closure
// over tracestory.BuildWithTraceStore). The trace selected is the first
// SampleTraces entry on the underlying incident; if none exists, returns an
// empty result rather than erroring (M1).
func NewStoryBuilderAdapter(r IncidentReader, build StoryBuildFunc) StoryBuilder {
	return storyBuilderAdapter{r: r, build: build}
}

func (a storyBuilderAdapter) FirstFailureStory(ctx context.Context, inc IncidentSummary, opts BuildOptions) (FirstFailureResult, error) {
	if inc.Propagation != nil && inc.Propagation.Latest != nil {
		return a.firstFailureFromSnapshot(inc)
	}
	return a.firstFailureFromReader(ctx, inc)
}

// firstFailureFromSnapshot projects from Incident.Propagation.Latest + Blast.Latest
// (spec: Report.SampleTraces ← Incident.Blast.Latest.SampledTraces; FirstFailure is
// a compact JSON object with origin_service / origin_step / first_failing_step /
// error_code / sample_trace_id).
func (a storyBuilderAdapter) firstFailureFromSnapshot(inc IncidentSummary) (FirstFailureResult, error) {
	p := inc.Propagation.Latest
	firstFailing, errCode := firstErrorStep(p)
	payload := struct {
		OriginService    string `json:"origin_service"`
		OriginStep       string `json:"origin_step"`
		FirstFailingStep string `json:"first_failing_step,omitempty"`
		ErrorCode        string `json:"error_code,omitempty"`
		SampleTraceID    string `json:"sample_trace_id,omitempty"`
	}{
		OriginService:    p.OriginService,
		OriginStep:       p.OriginStep,
		FirstFailingStep: firstFailing,
		ErrorCode:        errCode,
		SampleTraceID:    p.SampleTraceID,
	}
	raw, err := json.Marshal(&payload)
	if err != nil {
		return FirstFailureResult{}, fmt.Errorf("triage: project first failure: %w", err)
	}
	var samples []pkgtriage.TraceSample
	if inc.Blast != nil && inc.Blast.Latest != nil {
		for _, traceID := range inc.Blast.Latest.SampledTraces {
			summary := ""
			if traceID == p.SampleTraceID {
				summary = storySummaryFromPath(p)
			}
			samples = append(samples, pkgtriage.TraceSample{TraceID: traceID, Summary: summary})
		}
	}
	if len(samples) == 0 && p.SampleTraceID != "" {
		samples = []pkgtriage.TraceSample{{TraceID: p.SampleTraceID, Summary: storySummaryFromPath(p)}}
	}
	return FirstFailureResult{Payload: raw, SampleTraces: samples}, nil
}

// firstFailureFromReader is the pre-v1.0 computation path. Called when the
// incident has no Propagation.Latest snapshot.
func (a storyBuilderAdapter) firstFailureFromReader(ctx context.Context, inc IncidentSummary) (FirstFailureResult, error) {
	upstream, err := a.r.Get(ctx, inc.ID)
	if err != nil {
		if errors.Is(err, incidents.ErrNotFound) {
			return FirstFailureResult{}, nil
		}
		return FirstFailureResult{}, err
	}
	if len(upstream.SampleTraces) == 0 {
		return FirstFailureResult{}, nil
	}
	traceID := upstream.SampleTraces[0]
	resp, ok := a.build(traceID)
	if !ok {
		return FirstFailureResult{}, nil
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return FirstFailureResult{}, fmt.Errorf("triage: marshal story: %w", err)
	}
	summary := storySummary(resp, inc)
	return FirstFailureResult{
		Payload:      payload,
		SampleTraces: []pkgtriage.TraceSample{{TraceID: resp.TraceID, Summary: summary}},
	}, nil
}

// firstErrorStep walks p.Path and returns the first step with status="error"
// (step name + error code). Returns "","" if none.
func firstErrorStep(p *incidents.PropagationEvidence) (step, code string) {
	if p == nil {
		return "", ""
	}
	for _, s := range p.Path {
		if s.Status == "error" {
			return s.Step, s.ErrorCode
		}
	}
	return "", ""
}

func storySummaryFromPath(p *incidents.PropagationEvidence) string {
	if p == nil || len(p.Path) == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%s → %s", p.OriginService, p.OriginStep, p.Path[len(p.Path)-1].Step)
}

func storySummary(s apiv2.StoryResponse, inc IncidentSummary) string {
	svc := s.Service
	step := ""
	code := ""
	if s.Anchor != nil {
		step = s.Anchor.Step
		code = s.Anchor.ErrorCode
	}
	switch {
	case svc != "" && step != "" && code != "":
		return svc + "/" + step + "/" + code
	case svc != "" && code != "":
		return svc + " " + code
	case svc != "":
		return svc + " failure"
	case code != "":
		return code
	}
	if inc.Service != "" && inc.Step != "" && inc.ErrorCode != "" {
		return inc.Service + "/" + inc.Step + "/" + inc.ErrorCode
	}
	if inc.Service != "" && inc.ErrorCode != "" {
		return inc.Service + " " + inc.ErrorCode
	}
	return "first failure"
}

type signalQueryAdapter struct {
	s                SignalStore
	alertMatchWindow time.Duration
}

func NewSignalQueryAdapter(s SignalStore) SignalQuery {
	return signalQueryAdapter{s: s}
}

func NewAlertQueryAdapter(s SignalStore, matchWindow ...time.Duration) AlertQuery {
	window := 15 * time.Minute
	if len(matchWindow) > 0 && matchWindow[0] > 0 {
		window = matchWindow[0]
	}
	if window > 24*time.Hour {
		window = 24 * time.Hour
	}
	return signalQueryAdapter{s: s, alertMatchWindow: window}
}

func (a signalQueryAdapter) SignalsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]SignalEvidence, error) {
	end := opts.Now
	if end.IsZero() {
		end = inc.UpdatedAt
	}
	window := opts.Window
	if window <= 0 {
		window = defaultWindow
	}
	// Mirror incidents.Engine.querySignals: filter by env+window only. A
	// service filter would drop cross-service evidence (e.g. a payment
	// dependency signal on a checkout incident).
	rows, err := a.s.Query(ctx, signals.Filter{
		Env:   inc.Env,
		Since: end.Add(-window),
		Until: end,
		Limit: 200,
	})
	if err != nil {
		if errors.Is(err, signals.ErrUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SignalEvidence, 0, len(rows))
	for _, sig := range rows {
		out = append(out, SignalEvidence{
			ID:          sig.SignalID,
			Type:        string(sig.Type),
			EvidenceIDs: []string{sig.SignalID},
		})
	}
	return out, nil
}

func (a signalQueryAdapter) AlertsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]pkgtriage.AlertRef, error) {
	end := opts.Now
	if end.IsZero() {
		end = inc.UpdatedAt
	}
	since := inc.StartedAt.Add(-a.alertMatchWindow)
	if inc.StartedAt.IsZero() {
		window := opts.Window
		if window <= 0 {
			window = defaultWindow
		}
		since = end.Add(-window)
	}
	until := end.Add(a.alertMatchWindow)
	rows, err := a.s.Query(ctx, signals.Filter{
		Env:     inc.Env,
		Service: inc.Service,
		Types:   []signals.Type{signals.TypeAlert},
		Since:   since,
		Until:   until,
		Limit:   200,
	})
	if err != nil {
		if errors.Is(err, signals.ErrUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]pkgtriage.AlertRef, 0, len(rows))
	for _, sig := range rows {
		out = append(out, pkgtriage.AlertRef{
			SignalID:    sig.SignalID,
			AlertID:     stringField(sig.Metadata, "alert_id"),
			Source:      sig.Source,
			Severity:    string(sig.Severity),
			Reason:      sig.Reason,
			ProviderURL: stringField(sig.Metadata, "provider_url"),
			EvidenceIDs: []string{sig.SignalID},
		})
	}
	return out, nil
}

func stringField(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

type nextChecksAdapter struct{}

// NewNextChecksAdapter returns a passthrough that converts the incident's
// own NextChecks list (already populated by the incidents engine via
// internal/incidents.NextChecks(cause, confidence)) into the typed
// NextCheckSpec entries the report consumes. Stable IDs (check_<index>)
// keep the report deterministic across runs.
func NewNextChecksAdapter() NextChecksProvider {
	return nextChecksAdapter{}
}

func (nextChecksAdapter) NextChecks(_ context.Context, inc IncidentSummary) ([]NextCheckSpec, error) {
	if len(inc.NextChecks) == 0 {
		return nil, nil
	}
	out := make([]NextCheckSpec, 0, len(inc.NextChecks))
	for i, prompt := range inc.NextChecks {
		out = append(out, NextCheckSpec{ID: "check_" + strconv.Itoa(i), Prompt: prompt})
	}
	return out, nil
}
