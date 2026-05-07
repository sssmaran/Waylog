package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

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
		ID:         inc.IncidentID,
		Window:     defaultWindowLabel,
		Env:        inc.Env,
		StartedAt:  inc.StartedAt,
		UpdatedAt:  inc.UpdatedAt,
		Service:    inc.ErrorFamily.Service,
		Step:       inc.ErrorFamily.Step,
		ErrorCode:  inc.ErrorFamily.ErrorCode,
		Confidence: mapConfidence(inc.Confidence),
		NextChecks: append([]string(nil), inc.NextChecks...),
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
	end := opts.Now
	if end.IsZero() {
		end = inc.UpdatedAt
	}
	window := opts.Window
	if window <= 0 {
		window = defaultWindow
	}
	filter := incidents.SearchFilter{
		Service:   inc.Service,
		ErrorCode: inc.ErrorCode,
		Since:     end.Add(-window),
		Until:     end,
	}
	br := a.r.BlastRadius(filter, apiv2.BlastKey{
		Service:   inc.Service,
		Step:      inc.Step,
		ErrorCode: inc.ErrorCode,
	})
	users := 0
	if br.AffectedUsers != nil {
		users = *br.AffectedUsers
	}
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
	return BlastSnapshotResult{
		Requests:         br.AffectedRequests,
		Users:            users,
		Services:         br.AffectedServices,
		TopErrorFamilies: families,
	}, nil
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

func (a storyBuilderAdapter) FirstFailureStory(ctx context.Context, inc IncidentSummary, _ BuildOptions) (FirstFailureResult, error) {
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

type signalQueryAdapter struct{ s SignalStore }

func NewSignalQueryAdapter(s SignalStore) SignalQuery {
	return signalQueryAdapter{s: s}
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
