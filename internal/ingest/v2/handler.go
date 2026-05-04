package ingestv2

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

// Handler serves the schema-2.0 ingest contract mounted at POST /v1/events.
type Handler struct {
	schema  *jsonschema.Schema
	metrics *metrics.Metrics
	dedup   *Dedup
	wal     WAL
	project EventProjector
}

type WAL interface {
	WriteRaw([]byte) error
}

type Config struct {
	Metrics *metrics.Metrics
	Dedup   *Dedup
	WAL     WAL
	Index   *RecentIndex
	Project EventProjector
}

// New compiles the embedded v2.0 schema once for request-time reuse and
// pre-initializes the rejection-reason label series this handler emits so
// dashboards see zero-valued series before the first hit.
func New(cfg Config) (*Handler, error) {
	sch, err := eventv2.CompileEmbeddedSchema()
	if err != nil {
		return nil, err
	}
	m := cfg.Metrics
	if m != nil {
		for _, reason := range rejectionReasons {
			m.EventsRejected.WithLabelValues(reason).Add(0)
		}
	}
	dedup := cfg.Dedup
	if dedup == nil {
		var sizeGauge interface{ Set(float64) }
		if m != nil {
			sizeGauge = m.EventDedupCacheSize
		}
		dedup = NewDedup(DefaultDedupCapacity, sizeGauge)
	}
	projector := cfg.Project
	if projector == nil {
		index := cfg.Index
		if index == nil {
			var sizeGauge *prometheus.GaugeVec
			if m != nil {
				sizeGauge = m.V2IndexSize
			}
			index = NewRecentIndex(sizeGauge)
		}
		projector = NewProjector(index)
	}
	return &Handler{schema: sch, metrics: m, dedup: dedup, wal: cfg.WAL, project: projector}, nil
}

var (
	errDurabilityUnavailable = errors.New("durability unavailable")
	errProjectionUnavailable = errors.New("projection unavailable")
)

var rejectionReasons = []string{
	ReasonInvalidJSON,
	ReasonSchemaValidationFailed,
	ReasonBridgeNotImplemented,
	ReasonBatchOversize,
	ReasonBodyOversize,
	ReasonUnsupportedContentType,
	ReasonUnsupportedEncoding,
	ReasonInvalidBody,
	ReasonDurabilityUnavailable,
}

// Events validates schema-2.0 JSON/NDJSON ingest requests and returns the
// §5.1.2 envelope. Accepted events are written to the v2 WAL before response.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, true)
}

// Validate dry-runs schema-2.0 ingest using the same parser and envelope as
// Events, but without dedupe or WAL persistence.
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, false)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request, durable bool) {
	start := time.Now()
	if h.metrics != nil {
		h.metrics.InFlightRequests.Inc()
		defer h.metrics.InFlightRequests.Dec()
		defer func() { h.metrics.IngestLatency.Observe(time.Since(start).Seconds()) }()
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parsed, reqErr := parseRequestBody(w, r)
	if reqErr != nil {
		h.recordRejected(reqErr.reason)
		h.writeError(w, reqErr)
		return
	}
	env, err := h.IngestRaw(r.Context(), parsed.events, durable)
	if err != nil {
		switch {
		case errors.Is(err, errDurabilityUnavailable):
			http.Error(w, "durability unavailable", http.StatusServiceUnavailable)
		case errors.Is(err, errProjectionUnavailable):
			http.Error(w, "projection unavailable", http.StatusServiceUnavailable)
		default:
			http.Error(w, "ingest unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	writeEnvelope(w, http.StatusOK, env)
}

// IngestRaw validates already-framed schema-2.0 JSON events and optionally
// writes accepted events through the same dedupe, WAL, and projection path used
// by POST /v1/events. Per-event validation failures are represented in the
// returned envelope; infrastructure failures are returned as errors.
func (h *Handler) IngestRaw(ctx context.Context, eventBodies [][]byte, durable bool) (IngestEnvelope, error) {
	env := newEnvelope()
	if h.metrics != nil {
		h.metrics.IngestBatchSize.Observe(float64(len(eventBodies)))
	}
	for i, eventBody := range eventBodies {
		if err := ctx.Err(); err != nil {
			return env, err
		}
		validateStart := time.Now()
		result := h.validateEvent(i, eventBody)
		if h.metrics != nil {
			h.metrics.V2ValidateLatency.Observe(time.Since(validateStart).Seconds())
		}
		if !result.ok {
			env.Rejected = append(env.Rejected, result.rejected)
			h.recordRejected(result.rejected.Reason)
			continue
		}
		if !durable {
			env.Accepted++
			continue
		}
		if h.wal == nil {
			h.recordRejected(ReasonDurabilityUnavailable)
			return env, errDurabilityUnavailable
		}
		var duplicate bool
		var err error
		if h.dedup == nil {
			err = h.writeWAL(eventBody)
		} else {
			duplicate, err = h.dedup.AddIfNew(result.eventID, func() error {
				return h.writeWAL(eventBody)
			})
		}
		if err != nil {
			h.recordRejected(ReasonDurabilityUnavailable)
			return env, errDurabilityUnavailable
		}
		if duplicate {
			env.Duplicate++
			if h.metrics != nil {
				h.metrics.EventsDuplicate.Inc()
			}
			continue
		}
		if err := h.projectEvent(result.eventID, result.event); err != nil {
			return env, err
		}
		if h.metrics != nil {
			h.metrics.EventsAccepted.Inc()
		}
		env.Accepted++
	}
	return env, nil
}

type validationResult struct {
	ok       bool
	eventID  string
	event    *eventv2.Event
	rejected RejectedEvent
}

func (h *Handler) validateEvent(index int, eventBody []byte) validationResult {
	var raw any
	if err := json.Unmarshal(eventBody, &raw); err != nil {
		return validationResult{rejected: RejectedEvent{Index: index, Reason: ReasonInvalidJSON, Detail: err.Error()}}
	}

	eventID, schemaVersion := rawIdentifiers(raw)
	if strings.HasPrefix(schemaVersion, "1.") {
		return validationResult{eventID: eventID, rejected: RejectedEvent{Index: index, EventID: eventID, Reason: ReasonBridgeNotImplemented, Detail: "v1.x bridge ships in Slice 4"}}
	}
	if err := eventv2.ValidateAny(h.schema, raw); err != nil {
		return validationResult{eventID: eventID, rejected: RejectedEvent{Index: index, EventID: eventID, Reason: ReasonSchemaValidationFailed, Detail: err.Error()}}
	}
	var ev eventv2.Event
	if err := json.Unmarshal(eventBody, &ev); err != nil {
		if h.metrics != nil {
			h.metrics.V2TypedDecodeFailed.Inc()
		}
		return validationResult{eventID: eventID, rejected: RejectedEvent{Index: index, EventID: eventID, Reason: ReasonSchemaValidationFailed, Detail: "decode_typed: " + err.Error()}}
	}
	return validationResult{ok: true, eventID: eventID, event: &ev}
}

func rawIdentifiers(raw any) (eventID, schemaVersion string) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", ""
	}
	if id, ok := obj["event_id"].(string); ok {
		eventID = id
	}
	if version, ok := obj["schema_version"].(string); ok {
		schemaVersion = version
	}
	return eventID, schemaVersion
}

func (h *Handler) writeError(w http.ResponseWriter, reqErr *requestError) {
	env := newEnvelope()
	env.Rejected = append(env.Rejected, RejectedEvent{Index: 0, Reason: reqErr.reason, Detail: reqErr.detail})
	writeEnvelope(w, reqErr.status, env)
}

func (h *Handler) recordRejected(reason string) {
	if h.metrics != nil {
		h.metrics.EventsRejected.WithLabelValues(reason).Inc()
	}
}

func (h *Handler) writeWAL(eventBody []byte) error {
	start := time.Now()
	err := h.wal.WriteRaw(eventBody)
	if h.metrics != nil {
		h.metrics.V2WALWriteLatency.Observe(time.Since(start).Seconds())
	}
	return err
}

func (h *Handler) projectEvent(eventID string, ev *eventv2.Event) (err error) {
	start := time.Now()
	defer func() {
		if h.metrics != nil {
			h.metrics.V2ProjectLatency.Observe(time.Since(start).Seconds())
		}
		if recovered := recover(); recovered != nil {
			if h.metrics != nil {
				h.metrics.V2ProjectPanic.Inc()
			}
			if h.dedup != nil {
				h.dedup.Remove(eventID)
			}
			slog.Error("ingestv2: projection panic", "event_id", eventID, "panic", recovered)
			err = errProjectionUnavailable
		}
	}()
	if h.project != nil {
		h.project.Project(ev)
	}
	if h.metrics != nil {
		h.metrics.V2EventsProjected.Inc()
	}
	return nil
}

func writeEnvelope(w http.ResponseWriter, status int, env IngestEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
