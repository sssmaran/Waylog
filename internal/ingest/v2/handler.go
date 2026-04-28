package ingestv2

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

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
}

type WAL interface {
	WriteRaw([]byte) error
}

type Config struct {
	Metrics *metrics.Metrics
	Dedup   *Dedup
	WAL     WAL
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
	return &Handler{schema: sch, metrics: m, dedup: dedup, wal: cfg.WAL}, nil
}

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
	if h.metrics != nil {
		h.metrics.IngestBatchSize.Observe(float64(len(parsed.events)))
	}

	env := newEnvelope()
	for i, eventBody := range parsed.events {
		result := h.validateEvent(i, eventBody)
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
			http.Error(w, "durability unavailable", http.StatusServiceUnavailable)
			return
		}
		var duplicate bool
		var err error
		if h.dedup == nil {
			err = h.wal.WriteRaw(eventBody)
		} else {
			duplicate, err = h.dedup.AddIfNew(result.eventID, func() error {
				return h.wal.WriteRaw(eventBody)
			})
		}
		if err != nil {
			h.recordRejected(ReasonDurabilityUnavailable)
			http.Error(w, "durability unavailable", http.StatusServiceUnavailable)
			return
		}
		if duplicate {
			env.Duplicate++
			if h.metrics != nil {
				h.metrics.EventsDuplicate.Inc()
			}
			continue
		}
		if h.metrics != nil {
			h.metrics.EventsAccepted.Inc()
		}
		env.Accepted++
	}
	writeEnvelope(w, http.StatusOK, env)
}

type validationResult struct {
	ok       bool
	eventID  string
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
	return validationResult{ok: true, eventID: eventID}
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

func writeEnvelope(w http.ResponseWriter, status int, env IngestEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
