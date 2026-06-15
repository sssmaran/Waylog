package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

var ErrUnknownIncident = errors.New("triage: unknown incident")

// IncidentSummary is the minimal incident shape this package needs.
// Adapter types in the wiring layer convert from internal/incidents.Incident.
type IncidentSummary struct {
	ID          string
	Window      string
	Env         string
	StartedAt   time.Time
	UpdatedAt   time.Time
	Service     string
	Step        string
	ErrorCode   string
	Confidence  pkgtriage.Confidence
	NextChecks  []string
	Propagation *incidents.PropagationSnapshot
	Blast       *incidents.BlastSnapshot
	Alerts      *incidents.AlertSnapshot
	Runtime     *incidents.RuntimeSnapshot
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

type AlertQuery interface {
	AlertsFor(ctx context.Context, inc IncidentSummary, opts BuildOptions) ([]pkgtriage.AlertRef, error)
}

type NextChecksProvider interface {
	NextChecks(ctx context.Context, inc IncidentSummary) ([]NextCheckSpec, error)
}

type Deps struct {
	Incidents  IncidentLookup
	Blast      BlastQuery
	Story      StoryBuilder
	Signals    SignalQuery
	Alerts     AlertQuery
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
	alerts, fromSnapshot := alertsFromSnapshot(inc.Alerts)
	if !fromSnapshot && e.deps.Alerts != nil {
		alerts, err = e.deps.Alerts.AlertsFor(ctx, inc, opts)
		if err != nil {
			return nil, fmt.Errorf("triage: alerts: %w", err)
		}
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
		Alerts:       alerts,
		Runtime:      runtimeFromSnapshot(inc.Runtime),
		NextChecks:   checks,
		Confidence:   inc.Confidence,
		GeneratedAt:  e.deps.Now().UTC().Format(time.RFC3339Nano),
	}

	hash, err := r.CanonicalHash()
	if err != nil {
		return nil, fmt.Errorf("triage: hash: %w", err)
	}
	r.ReportHash = hash
	r.EvidenceFingerprint = r.CanonicalEvidenceFingerprint()
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("triage: produced invalid report: %w", err)
	}
	return r, nil
}

func alertsFromSnapshot(s *incidents.AlertSnapshot) ([]pkgtriage.AlertRef, bool) {
	if s == nil || s.Latest == nil {
		return nil, false
	}
	out := make([]pkgtriage.AlertRef, 0, len(s.Latest.Matches))
	for _, m := range s.Latest.Matches {
		evidenceIDs := append([]string(nil), m.EvidenceIDs...)
		if len(evidenceIDs) == 0 && m.SignalID != "" {
			evidenceIDs = []string{m.SignalID}
		}
		out = append(out, pkgtriage.AlertRef{
			SignalID:    m.SignalID,
			AlertID:     m.AlertID,
			Source:      m.Source,
			Severity:    m.Severity,
			Reason:      m.Reason,
			ProviderURL: m.ProviderURL,
			EvidenceIDs: evidenceIDs,
		})
	}
	return out, true
}

// runtimeFromSnapshot projects all matched runtime evidence (infra AND app)
// into report RuntimeRefs. Uses RuntimeSnapshot.Matches (not Opening/Latest) so
// both infra and app rows survive into the report. OccurredAt is stable, so the
// rows participate in report_hash; CapturedAt is deliberately excluded.
func runtimeFromSnapshot(s *incidents.RuntimeSnapshot) []pkgtriage.RuntimeRef {
	if s == nil || len(s.Matches) == 0 {
		return nil
	}
	out := make([]pkgtriage.RuntimeRef, 0, len(s.Matches))
	for _, m := range s.Matches {
		occurred := ""
		if !m.OccurredAt.IsZero() {
			occurred = m.OccurredAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, pkgtriage.RuntimeRef{
			SignalID:   m.SignalID,
			Subtype:    m.Subtype,
			Service:    m.Service,
			Source:     m.Source,
			Severity:   m.Severity,
			Reason:     m.Reason,
			OccurredAt: occurred,
		})
	}
	return out
}
