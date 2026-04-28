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
}

// New compiles the embedded v2.0 schema once for request-time reuse and
// pre-initializes the rejection-reason label series this handler emits so
// dashboards see zero-valued series before the first hit.
func New(m *metrics.Metrics) (*Handler, error) {
	sch, err := eventv2.CompileEmbeddedSchema()
	if err != nil {
		return nil, err
	}
	if m != nil {
		for _, reason := range rejectionReasons {
			m.EventsRejected.WithLabelValues(reason).Add(0)
		}
	}
	return &Handler{schema: sch, metrics: m}, nil
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
}

// Events validates schema-2.0 JSON/NDJSON ingest requests and returns the
// §5.1.2 envelope. Slice 1 validates-and-discards; durability lands in Slice 2.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
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
		rej, ok := h.validateEvent(i, eventBody)
		if ok {
			env.Accepted++
			continue
		}
		env.Rejected = append(env.Rejected, rej)
		h.recordRejected(rej.Reason)
	}
	writeEnvelope(w, http.StatusOK, env)
}

func (h *Handler) validateEvent(index int, eventBody []byte) (RejectedEvent, bool) {
	var raw any
	if err := json.Unmarshal(eventBody, &raw); err != nil {
		return RejectedEvent{Index: index, Reason: ReasonInvalidJSON, Detail: err.Error()}, false
	}

	eventID, schemaVersion := rawIdentifiers(raw)
	if strings.HasPrefix(schemaVersion, "1.") {
		return RejectedEvent{Index: index, EventID: eventID, Reason: ReasonBridgeNotImplemented, Detail: "v1.x bridge ships in Slice 4"}, false
	}
	if err := eventv2.ValidateAny(h.schema, raw); err != nil {
		return RejectedEvent{Index: index, EventID: eventID, Reason: ReasonSchemaValidationFailed, Detail: err.Error()}, false
	}
	return RejectedEvent{}, true
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
