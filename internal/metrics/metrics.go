package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var defaultBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

// Metrics holds all Prometheus collectors for the ingest server.
type Metrics struct {
	reg *prometheus.Registry

	IngestLatency          prometheus.Histogram
	IngestBatchSize        prometheus.Histogram
	EventsAccepted         prometheus.Counter
	EventsDuplicate        prometheus.Counter
	EventsRejected         *prometheus.CounterVec
	RateLimited            *prometheus.CounterVec
	EventlogFails          prometheus.Counter
	EventDedupCacheSize    prometheus.Gauge
	EventDedupReplayLoaded prometheus.Counter
	V2EventsProjected      prometheus.Counter
	V2IndexSize            *prometheus.GaugeVec
	V2IndexPruned          prometheus.Counter
	V2ReplayProjected      prometheus.Counter
	V2ReplaySkipped        *prometheus.CounterVec
	V2TypedDecodeFailed    prometheus.Counter
	V2ProjectPanic         prometheus.Counter
	V2ValidateLatency      prometheus.Histogram
	V2WALWriteLatency      prometheus.Histogram
	V2ProjectLatency       prometheus.Histogram
	V2ReadLatency          *prometheus.HistogramVec
	V2ReadEmpty            *prometheus.CounterVec
	V2ReadNotFound         *prometheus.CounterVec

	ReplayLagSeconds    prometheus.Gauge
	ReplayInProgress    prometheus.Gauge
	ReplayFailuresTotal prometheus.Counter
	Ready               prometheus.Gauge
	InFlightRequests    prometheus.Gauge

	AskRequestsTotal     *prometheus.CounterVec
	AskDuration          prometheus.Histogram
	AskToolCallsTotal    *prometheus.CounterVec
	AskToolDuration      *prometheus.HistogramVec
	ToolDirectCallsTotal *prometheus.CounterVec
	DedupReplayTotal     prometheus.Counter
	DedupCacheSize       prometheus.Gauge

	ColdEventsWritten prometheus.Counter
	ColdEventsDropped prometheus.Counter
	ColdBatchLatency  prometheus.Histogram

	DeployUpsertsTotal prometheus.Counter
	DeployUpsertErrors prometheus.Counter

	SignalsAccepted         prometheus.Counter
	SignalsRejected         *prometheus.CounterVec
	SignalRetentionPruned   prometheus.Counter
	IncidentRetentionPruned prometheus.Counter

	IncidentOpened          prometheus.Counter
	IncidentUpdated         prometheus.Counter
	IncidentRecovered       prometheus.Counter
	IncidentResolved        prometheus.Counter
	IncidentTickLatency     prometheus.Histogram
	IncidentActive          prometheus.Gauge
	IncidentClassifications *prometheus.CounterVec
	IncidentRebuildDuration prometheus.Histogram
	IncidentRebuildRows     prometheus.Counter
	IncidentRebuildFailures prometheus.Counter
	IncidentRebuildReplayed prometheus.Counter

	// OTLP ingestion metrics
	OTLPRequestsTotal     *prometheus.CounterVec // labels: status
	OTLPSpansReceived     prometheus.Counter
	OTLPSpansConverted    prometheus.Counter
	OTLPSpansDropped      *prometheus.CounterVec // labels: reason
	OTLPValidationRejects prometheus.Counter
	OTLPDecodeFailures    prometheus.Counter
	OTLPInfraFailures     prometheus.Counter
	OTLPRequestDuration   prometheus.Histogram
	OTLPRequestSizeBytes  prometheus.Histogram
}

// New creates a Metrics instance and registers all collectors with the given registry.
func New(reg *prometheus.Registry) *Metrics {
	m := &Metrics{reg: reg}

	m.IngestLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_ingest_latency_seconds",
		Help:    "Full Events handler latency, including parsing and response encoding. For v2 sub-step latency see waylog_v2_*_latency_seconds.",
		Buckets: defaultBuckets,
	})
	m.IngestBatchSize = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_ingest_batch_size",
		Help:    "Number of events parsed from each ingest request.",
		Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256},
	})
	m.EventsAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_events_accepted_total",
		Help: "Events accepted after each ingest path's durability contract is satisfied.",
	})
	m.EventsDuplicate = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_events_duplicate_total",
		Help: "Schema-2.0 ingest events skipped because event_id was already recently durably written.",
	})
	m.EventsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_events_rejected_total",
		Help: "Dropped events.",
	}, []string{"reason"})
	for _, reason := range []string{"validation", "sampling"} {
		m.EventsRejected.WithLabelValues(reason).Add(0)
	}
	m.RateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_rate_limited_total",
		Help: "Requests rejected with 429 by the per-key rate limiter.",
	}, []string{"scope"})
	m.EventlogFails = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_eventlog_write_failures_total",
		Help: "Failed eventlog writes.",
	})
	m.EventDedupCacheSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_event_dedup_cache_size",
		Help: "Current schema-2.0 event_id dedup cache size.",
	})
	m.EventDedupReplayLoaded = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_event_dedup_replay_loaded_total",
		Help: "Schema-2.0 event IDs loaded into dedup cache during WAL replay.",
	})
	m.V2EventsProjected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_v2_events_projected_total",
		Help: "Schema-2.0 events projected into the recent index from live ingest.",
	})
	m.V2IndexSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "waylog_v2_index_size",
		Help: "Current schema-2.0 recent index size by kind.",
	}, []string{"kind"})
	for _, kind := range []string{"event", "trace", "service", "error", "call"} {
		m.V2IndexSize.WithLabelValues(kind).Set(0)
	}
	m.V2IndexPruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_v2_index_pruned_total",
		Help: "Schema-2.0 events pruned from the recent index.",
	})
	m.V2ReplayProjected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_v2_replay_projected_total",
		Help: "Schema-2.0 events projected into the recent index during WAL replay.",
	})
	m.V2ReplaySkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_v2_replay_skipped_total",
		Help: "Schema-2.0 WAL replay lines skipped by reason.",
	}, []string{"reason"})
	for _, reason := range []string{"malformed_json", "schema_invalid", "typed_decode", "stale"} {
		m.V2ReplaySkipped.WithLabelValues(reason).Add(0)
	}
	m.V2TypedDecodeFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_v2_typed_decode_failed_total",
		Help: "Schema-2.0 events that passed raw schema validation but failed typed decode.",
	})
	m.V2ProjectPanic = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_v2_project_panic_total",
		Help: "Recovered panics while projecting schema-2.0 events into the recent index.",
	})
	m.V2ValidateLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_v2_validate_latency_seconds",
		Help:    "Schema-2.0 per-event raw validation and typed decode latency.",
		Buckets: defaultBuckets,
	})
	m.V2WALWriteLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_v2_wal_write_latency_seconds",
		Help:    "Schema-2.0 per-event WAL write latency.",
		Buckets: defaultBuckets,
	})
	m.V2ProjectLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_v2_project_latency_seconds",
		Help:    "Schema-2.0 per-event recent-index projection latency.",
		Buckets: defaultBuckets,
	})
	m.V2ReadLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "waylog_v2_read_latency_seconds",
		Help:    "Schema-2.0 read endpoint latency by handler.",
		Buckets: defaultBuckets,
	}, []string{"handler"})
	m.V2ReadEmpty = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_v2_read_empty_total",
		Help: "Schema-2.0 read endpoint 200 responses with empty result arrays by handler.",
	}, []string{"handler"})
	m.V2ReadNotFound = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_v2_read_not_found_total",
		Help: "Schema-2.0 read endpoint 404 responses by handler.",
	}, []string{"handler"})
	for _, handler := range []string{"event_get", "event_search", "trace_get", "traces_recent", "trace_story", "errors", "blast_radius"} {
		m.V2ReadLatency.WithLabelValues(handler).Observe(0)
		m.V2ReadEmpty.WithLabelValues(handler).Add(0)
		m.V2ReadNotFound.WithLabelValues(handler).Add(0)
	}

	m.ReplayLagSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_replay_lag_seconds",
		Help: "Seconds since last replayed event; 0 after done.",
	})
	m.ReplayInProgress = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_replay_in_progress",
		Help: "1 during startup replay, 0 after.",
	})
	m.ReplayFailuresTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_replay_failures_total",
		Help: "Number of failed startup replay attempts.",
	})
	m.Ready = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_ready",
		Help: "1 when ready, 0 otherwise.",
	})
	m.InFlightRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_inflight_requests",
		Help: "Concurrent Events handler calls.",
	})
	m.AskRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_ask_requests_total",
		Help: "Ask endpoint requests.",
	}, []string{"status", "error_code"})
	m.AskDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_ask_duration_seconds",
		Help:    "Ask endpoint latency.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
	})
	m.AskToolCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_ask_tool_calls_total",
		Help: "Tool calls within ask.",
	}, []string{"tool", "status"})
	m.AskToolDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "waylog_ask_tool_duration_seconds",
		Help:    "Tool call latency within ask.",
		Buckets: defaultBuckets,
	}, []string{"tool"})
	m.ToolDirectCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_tool_direct_calls_total",
		Help: "Direct tool endpoint calls.",
	}, []string{"tool", "status"})
	// Renamed from waylog_dedup_cache_hits_total → waylog_dedup_replay_total
	// to reflect that this counts both cache-hit replays and inflight-wait replays.
	m.DedupReplayTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_dedup_replay_total",
		Help: "Idempotency replay responses (cache hit or inflight wait).",
	})
	m.DedupCacheSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_dedup_cache_size",
		Help: "Current dedup cache entry count.",
	})

	m.ColdEventsWritten = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_cold_events_written_total",
		Help: "Events successfully written to cold store.",
	})
	m.ColdEventsDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_cold_events_dropped_total",
		Help: "Events dropped due to full cold store queue.",
	})
	m.ColdBatchLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_cold_batch_latency_seconds",
		Help:    "Cold store batch insert latency.",
		Buckets: defaultBuckets,
	})

	m.DeployUpsertsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_deploy_upserts_total",
		Help: "Successful deployment upserts.",
	})
	m.DeployUpsertErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_deploy_upsert_errors_total",
		Help: "Failed deployment upserts (non-env-conflict).",
	})

	m.SignalsAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_signals_accepted_total",
		Help: "Production-context signals accepted into durable storage.",
	})
	m.SignalsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_signals_rejected_total",
		Help: "Production-context signals rejected by reason.",
	}, []string{"reason"})
	for _, reason := range []string{
		"invalid_field", "unknown_type", "unknown_severity", "timestamp_too_far_in_future",
		"body_oversize", "invalid_body", "invalid_json", "unsupported_method",
		"durability_unavailable", "internal_error",
	} {
		m.SignalsRejected.WithLabelValues(reason).Add(0)
	}
	m.SignalRetentionPruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_signal_retention_pruned_total",
		Help: "Production-context signals pruned by retention.",
	})
	m.IncidentRetentionPruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incident_retention_pruned_total",
		Help: "Resolved incidents pruned by retention.",
	})

	m.IncidentOpened = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incidents_opened_total",
		Help: "Incidents opened by the v2.1 incident engine.",
	})
	m.IncidentUpdated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incidents_updated_total",
		Help: "Incidents updated by the v2.1 incident engine.",
	})
	m.IncidentRecovered = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incidents_recovered_total",
		Help: "Incidents moved to recovering by the v2.1 incident engine.",
	})
	m.IncidentResolved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incidents_resolved_total",
		Help: "Incidents resolved by the v2.1 incident engine.",
	})
	m.IncidentTickLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_incident_tick_latency_seconds",
		Help:    "Incident engine tick duration.",
		Buckets: defaultBuckets,
	})
	m.IncidentActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_incidents_active",
		Help: "Active or recovering incidents currently tracked by the v2.1 incident engine.",
	})
	m.IncidentClassifications = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_incident_classifications_total",
		Help: "Incident classifications by cause and confidence.",
	}, []string{"cause", "confidence"})
	for _, cause := range []string{"deploy", "app", "dependency", "runtime", "unknown"} {
		for _, confidence := range []string{"high", "medium", "low"} {
			m.IncidentClassifications.WithLabelValues(cause, confidence).Add(0)
		}
	}
	m.IncidentRebuildDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_incident_rebuild_duration_seconds",
		Help:    "Startup hot-window incident rebuild duration.",
		Buckets: defaultBuckets,
	})
	m.IncidentRebuildRows = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incident_rebuild_rows_replaced",
		Help: "Incident rows replaced by startup hot-window rebuild.",
	})
	m.IncidentRebuildFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incident_rebuild_failures_total",
		Help: "Failed startup hot-window incident rebuild attempts.",
	})
	m.IncidentRebuildReplayed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_incident_rebuild_replayed_events_total",
		Help: "Schema-2.0 events replayed for startup hot-window incident rebuild.",
	})

	m.OTLPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_otlp_requests_total",
		Help: "Total OTLP trace ingestion requests.",
	}, []string{"status"})
	m.OTLPSpansReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_otlp_spans_received_total",
		Help: "Total spans in decoded OTLP requests.",
	})
	m.OTLPSpansConverted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_otlp_spans_converted_total",
		Help: "Spans successfully converted to WideEvents.",
	})
	m.OTLPSpansDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_otlp_spans_dropped_total",
		Help: "Spans the converter could not convert.",
	}, []string{"reason"})
	m.OTLPValidationRejects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_otlp_validation_rejects_total",
		Help: "Converted events that failed validation.",
	})
	m.OTLPDecodeFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_otlp_decode_failures_total",
		Help: "Protobuf decode failures.",
	})
	m.OTLPInfraFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_otlp_infra_failures_total",
		Help: "WAL/cold store failures during OTLP ingest.",
	})
	m.OTLPRequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_otlp_request_duration_seconds",
		Help:    "OTLP endpoint latency.",
		Buckets: defaultBuckets,
	})
	m.OTLPRequestSizeBytes = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_otlp_request_size_bytes",
		Help:    "OTLP request body size post-decompression.",
		Buckets: prometheus.ExponentialBuckets(1024, 4, 8),
	})

	reg.MustRegister(
		m.IngestLatency, m.IngestBatchSize,
		m.EventsAccepted, m.EventsDuplicate, m.EventsRejected, m.RateLimited, m.EventlogFails,
		m.EventDedupCacheSize, m.EventDedupReplayLoaded,
		m.V2EventsProjected, m.V2IndexSize, m.V2IndexPruned, m.V2ReplayProjected,
		m.V2ReplaySkipped,
		m.V2TypedDecodeFailed, m.V2ProjectPanic,
		m.V2ValidateLatency, m.V2WALWriteLatency, m.V2ProjectLatency,
		m.V2ReadLatency, m.V2ReadEmpty, m.V2ReadNotFound,
		m.ReplayLagSeconds, m.ReplayInProgress, m.ReplayFailuresTotal, m.Ready,
		m.InFlightRequests,
		m.AskRequestsTotal, m.AskDuration,
		m.AskToolCallsTotal, m.AskToolDuration,
		m.ToolDirectCallsTotal, m.DedupReplayTotal, m.DedupCacheSize,
		m.ColdEventsWritten, m.ColdEventsDropped, m.ColdBatchLatency,
		m.DeployUpsertsTotal, m.DeployUpsertErrors,
		m.SignalsAccepted, m.SignalsRejected, m.SignalRetentionPruned, m.IncidentRetentionPruned,
		m.IncidentOpened, m.IncidentUpdated, m.IncidentRecovered, m.IncidentResolved,
		m.IncidentTickLatency, m.IncidentActive, m.IncidentClassifications,
		m.IncidentRebuildDuration, m.IncidentRebuildRows, m.IncidentRebuildFailures, m.IncidentRebuildReplayed,
		m.OTLPRequestsTotal, m.OTLPSpansReceived, m.OTLPSpansConverted,
		m.OTLPSpansDropped, m.OTLPValidationRejects, m.OTLPDecodeFailures,
		m.OTLPInfraFailures, m.OTLPRequestDuration, m.OTLPRequestSizeBytes,
	)

	return m
}

// Handler returns an http.Handler that serves Prometheus metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
