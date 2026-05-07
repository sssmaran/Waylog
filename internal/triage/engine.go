package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

var ErrUnknownIncident = errors.New("triage: unknown incident")

// IncidentSummary is the minimal incident shape this package needs.
// Adapter types in the wiring layer convert from internal/incidents.Incident.
type IncidentSummary struct {
	ID         string
	Window     string
	Env        string
	StartedAt  time.Time
	UpdatedAt  time.Time
	Service    string
	Step       string
	ErrorCode  string
	Confidence pkgtriage.Confidence
	NextChecks []string
}

type BlastSnapshotResult struct {
	Requests         int
	Users            int
	Services         int
	TopErrorFamilies []pkgtriage.ErrorFamily
}

type FirstFailureResult struct {
	Payload      json.RawMessage
	SampleTraces []pkgtriage.TraceSample
}

type SignalEvidence = pkgtriage.SignalRef

type NextCheckSpec = pkgtriage.NextCheck

type IncidentLookup interface {
	GetIncident(ctx context.Context, id string) (IncidentSummary, error)
}

type BlastQuery interface {
	BlastSnapshot(ctx context.Context, inc IncidentSummary, opts BuildOptions) (BlastSnapshotResult, error)
}

type StoryBuilder interface {
	FirstFailureStory(ctx context.Context, inc IncidentSummary, opts BuildOptions) (FirstFailureResult, error)
}

type SignalQuery interface {
	SignalsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]SignalEvidence, error)
}

type NextChecksProvider interface {
	NextChecks(ctx context.Context, inc IncidentSummary) ([]NextCheckSpec, error)
}

type Deps struct {
	Incidents  IncidentLookup
	Blast      BlastQuery
	Story      StoryBuilder
	Signals    SignalQuery
	NextChecks NextChecksProvider
	Now        func() time.Time
}

type Engine struct {
	deps Deps
}

func NewEngine(d Deps) (*Engine, error) {
	if d.Incidents == nil || d.Blast == nil || d.Story == nil || d.Signals == nil || d.NextChecks == nil {
		return nil, fmt.Errorf("triage: NewEngine requires all dependencies")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Engine{deps: d}, nil
}

func (e *Engine) Build(ctx context.Context, incidentID string, opts BuildOptions) (*pkgtriage.Report, error) {
	inc, err := e.deps.Incidents.GetIncident(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if opts.Snapshot {
		opts.Now = inc.UpdatedAt
	}

	blast, err := e.deps.Blast.BlastSnapshot(ctx, inc, opts)
	if err != nil {
		return nil, fmt.Errorf("triage: blast: %w", err)
	}
	story, err := e.deps.Story.FirstFailureStory(ctx, inc, opts)
	if err != nil {
		return nil, fmt.Errorf("triage: story: %w", err)
	}
	sigs, err := e.deps.Signals.SignalsFor(ctx, inc, opts)
	if err != nil {
		return nil, fmt.Errorf("triage: signals: %w", err)
	}
	checks, err := e.deps.NextChecks.NextChecks(ctx, inc)
	if err != nil {
		return nil, fmt.Errorf("triage: next_checks: %w", err)
	}

	r := &pkgtriage.Report{
		SchemaVersion: pkgtriage.SchemaVersionV1,
		IncidentRef:   pkgtriage.IncidentRef{ID: inc.ID, Window: opts.Window.String()},
		BlastSnapshot: pkgtriage.BlastSnapshot{
			Requests: blast.Requests, Users: blast.Users, Services: blast.Services,
			TopErrorFamilies: blast.TopErrorFamilies,
		},
		FirstFailure: story.Payload,
		SampleTraces: story.SampleTraces,
		Signals:      sigs,
		NextChecks:   checks,
		Confidence:   inc.Confidence,
		GeneratedAt:  e.deps.Now().UTC().Format(time.RFC3339Nano),
	}

	hash, err := r.CanonicalHash()
	if err != nil {
		return nil, fmt.Errorf("triage: hash: %w", err)
	}
	r.ReportHash = hash
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("triage: produced invalid report: %w", err)
	}
	return r, nil
}
