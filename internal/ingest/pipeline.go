package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// Notifier is a narrow interface for post-batch notification.
// Matches SSEHub.MarkDirty. nil-safe — Pipeline operates normally if nil.
type Notifier interface {
	MarkDirty(topics ...string)
}

// Validator is the per-event validation function the Pipeline applies before
// any durable work. SDK pipeline uses event.WideEvent.Validate; OTLP pipeline
// uses OTLPValidator which suppresses user.id-only failures.
type Validator func(ev *event.WideEvent) error

// PipelineConfig holds all dependencies for a Pipeline instance.
// Most fields are optional — the pipeline degrades gracefully when a
// dependency is nil (e.g., no EventLog means no WAL write).
type PipelineConfig struct {
	Store      *store.Store
	TraceStore *tracestore.Store
	Builder    *build.Builder
	Sampler    *sampler.Sampler
	EventLog   *eventlog.Writer
	ColdWriter *coldstore.BatchWriter
	ColdStore  coldstore.Store
	Counters   *unsampledCounters
	Accepted   *atomic.Uint64
	Metrics    *metrics.Metrics
	Notifier   Notifier
	Validator  Validator
}

// Pipeline is the protocol-agnostic ingest core shared by the SDK HTTP handler
// and the OTLP handler. Order of operations per event:
//
//	validate → WAL → counters → cold store → deployment upsert → sample →
//	build → merge graph + tracestore → notify (once per batch)
type Pipeline struct {
	store      *store.Store
	traceStore *tracestore.Store
	builder    *build.Builder
	sampler    *sampler.Sampler
	eventLog   *eventlog.Writer
	coldWriter *coldstore.BatchWriter
	coldStore  coldstore.Store
	counters   *unsampledCounters
	accepted   *atomic.Uint64
	metrics    *metrics.Metrics
	notifier   Notifier
	validator  Validator
}

// BatchResult reports per-event outcomes for a single batch submission.
type BatchResult struct {
	Accepted       int          // passed validation AND durably written
	SampledInGraph int          // accepted AND merged into graph/tracestore
	SampledOut     int          // accepted but dropped by sampler
	Rejected       int          // failed validation
	Errors         []EventError // per-event validation failures
}

// EventError describes a single rejected event.
type EventError struct {
	Index  int    // position in the input slice
	Reason string // validation failure reason
}

// NewPipeline creates a Pipeline from the given configuration.
func NewPipeline(cfg PipelineConfig) *Pipeline {
	return &Pipeline{
		store:      cfg.Store,
		traceStore: cfg.TraceStore,
		builder:    cfg.Builder,
		sampler:    cfg.Sampler,
		eventLog:   cfg.EventLog,
		coldWriter: cfg.ColdWriter,
		coldStore:  cfg.ColdStore,
		counters:   cfg.Counters,
		accepted:   cfg.Accepted,
		metrics:    cfg.Metrics,
		notifier:   cfg.Notifier,
		validator:  cfg.Validator,
	}
}

// ValidateAndIngestBatch validates every event in the batch, then ingests
// those that pass. Invalid events are counted in BatchResult.Rejected and
// reported via BatchResult.Errors. Only infrastructure failures (WAL, ctx)
// return a non-nil error — validation failures do not.
func (p *Pipeline) ValidateAndIngestBatch(ctx context.Context, events []*event.WideEvent) (BatchResult, error) {
	var result BatchResult
	valid := make([]*event.WideEvent, 0, len(events))

	for i, ev := range events {
		if p.validator != nil {
			if err := p.validator(ev); err != nil {
				result.Rejected++
				result.Errors = append(result.Errors, EventError{Index: i, Reason: err.Error()})
				if p.metrics != nil {
					p.metrics.EventsRejected.WithLabelValues("validation").Inc()
				}
				continue
			}
		}
		valid = append(valid, ev)
	}

	if len(valid) == 0 {
		return result, nil
	}

	sub, err := p.IngestBatch(ctx, valid)
	result.Accepted = sub.Accepted
	result.SampledInGraph = sub.SampledInGraph
	result.SampledOut = sub.SampledOut
	return result, err
}

// IngestBatch runs the durable pipeline over a pre-validated batch.
// Returns (result, nil) on success. Returns (partialResult, err) if an
// infrastructure failure (WAL write, ctx cancel) aborts the batch mid-flight;
// events processed before the failure remain in result counts.
func (p *Pipeline) IngestBatch(ctx context.Context, events []*event.WideEvent) (BatchResult, error) {
	var result BatchResult

	for _, ev := range events {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		sampled := true
		if p.sampler != nil {
			sampled = p.sampler.ShouldKeep(*ev)
		}

		// WAL: durable source of truth. Must succeed before anything else.
		if p.eventLog != nil {
			if err := p.eventLog.Write(ev, sampled); err != nil {
				slog.Error("pipeline: eventlog write failed", "err", err)
				if p.metrics != nil {
					p.metrics.EventlogFails.Inc()
				}
				return result, err
			}
		}

		result.Accepted++
		if p.metrics != nil && p.eventLog != nil {
			p.metrics.EventsAccepted.Inc()
		}

		// Windowed counters — post-WAL so WAL failures are never counted.
		if p.counters != nil {
			p.counters.Inc(!ev.Outcome.Success)
		}

		// Cold store enqueue (all accepted events, before sampling gate).
		if p.coldWriter != nil {
			p.coldWriter.Enqueue(*ev)
		}

		// Auto-extract deployment (detached context — event is already durable).
		if ev.System.DeploymentID != "" && p.coldStore != nil {
			upsertCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			err := p.coldStore.UpsertDeployment(upsertCtx, coldstore.Deployment{
				ID:        ev.System.DeploymentID,
				Service:   ev.System.Service,
				Version:   ev.System.Version,
				Env:       ev.System.Env,
				FirstSeen: ev.Timestamp,
				LastSeen:  ev.Timestamp,
			})
			cancel()
			if err != nil {
				if !errors.Is(err, coldstore.ErrEnvConflict) && p.metrics != nil {
					p.metrics.DeployUpsertErrors.Inc()
				}
				slog.Warn("pipeline: deployment auto-extract failed",
					"deployment_id", ev.System.DeploymentID, "err", err)
			} else if p.metrics != nil {
				p.metrics.DeployUpsertsTotal.Inc()
			}
		}

		if !sampled {
			result.SampledOut++
			if p.metrics != nil {
				p.metrics.EventsRejected.WithLabelValues("sampling").Inc()
			}
			continue
		}

		// Build graph + span and merge into derived views.
		if p.builder != nil {
			mergeStart := time.Now()
			br := p.builder.BuildResult(*ev)
			if p.store != nil && br.Graph != nil {
				p.store.Merge(br.Graph)
			}
			if p.traceStore != nil && br.Span != nil {
				traceStart := time.Now()
				p.traceStore.Upsert(ev.Request.TraceID, core.ID("request", ev.Request.TraceID), br.Span)
				if p.metrics != nil {
					p.metrics.TraceUpsertDuration.Observe(time.Since(traceStart).Seconds())
				}
			}
			if p.metrics != nil {
				p.metrics.MergeLatency.Observe(time.Since(mergeStart).Seconds())
			}
		}

		if p.accepted != nil {
			p.accepted.Add(1)
		}

		result.SampledInGraph++
	}

	if p.notifier != nil && result.SampledInGraph > 0 {
		p.notifier.MarkDirty(TopicOverview, TopicRoutes, TopicTimeseries)
	}

	return result, nil
}

// OTLPValidator is the Validator the OTLP pipeline uses. It runs the full
// event.WideEvent.Validate and then suppresses the result if the ONLY failing
// field is user.id — because OTLP has no standard way to carry end-user id.
// Any other validation failure (including multi-field errors that happen to
// include user.id) is returned unchanged.
func OTLPValidator(ev *event.WideEvent) error {
	if err := ev.Validate(); err != nil {
		var ve event.ValidationErrors
		if errors.As(err, &ve) && ve.HasOnly("user.id") {
			return nil
		}
		return err
	}
	return nil
}
