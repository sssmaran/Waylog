// Package ingestv2 implements the schema-2.0 ingest endpoint at POST /v1/events
// per docs/v2-plan.md §5.1.1 and §5.1.2.
package ingestv2

// IngestEnvelope is the response body §5.1.2 defines for POST /v1/events on
// 200 and on the partial-failure 4xx paths that still return per-event
// rejection detail. Both Rejected and Deprecations must be non-nil on the
// wire so SDK envelope parsers see [] / {} consistently.
type IngestEnvelope struct {
	Accepted     int             `json:"accepted"`
	Duplicate    int             `json:"duplicate"`
	Rejected     []RejectedEvent `json:"rejected"`
	Deprecations map[string]int  `json:"deprecations"`
}

// RejectedEvent describes one rejected entry inside a batch. Index is the
// zero-based position in the submitted batch. EventID is best-effort: the
// handler extracts it from the raw decoded body so callers can correlate
// even when validation failed.
type RejectedEvent struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail,omitempty"`
}

// Reason codes returned in RejectedEvent.Reason. Stable, machine-readable
// strings; SDK consumers route on these.
const (
	ReasonInvalidJSON            = "invalid_json"
	ReasonSchemaValidationFailed = "schema_validation_failed"
	ReasonBridgeNotImplemented   = "bridge_not_implemented"
	ReasonBatchOversize          = "batch_oversize"
	ReasonBodyOversize           = "body_oversize"
	ReasonUnsupportedContentType = "unsupported_content_type"
	ReasonUnsupportedEncoding    = "unsupported_content_encoding"
	ReasonInvalidBody            = "invalid_body"
	ReasonDurabilityUnavailable  = "durability_unavailable"
)

// newEnvelope returns a fresh response envelope with Rejected and
// Deprecations pre-allocated so JSON encoding emits [] / {} rather than null.
func newEnvelope() IngestEnvelope {
	return IngestEnvelope{
		Rejected:     []RejectedEvent{},
		Deprecations: map[string]int{},
	}
}
