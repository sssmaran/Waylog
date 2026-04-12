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

	IngestLatency  prometheus.Histogram
	MergeLatency   prometheus.Histogram
	EventsAccepted prometheus.Counter
	EventsRejected *prometheus.CounterVec
	EventlogFails  prometheus.Counter

	ReplayLagSeconds    prometheus.Gauge
	ReplayInProgress    prometheus.Gauge
	ReplayFailuresTotal prometheus.Counter
	Ready               prometheus.Gauge
	InFlightRequests    prometheus.Gauge
	SnapshotLastSuccess prometheus.Gauge
	SnapshotLastError   prometheus.Gauge
	GraphNodes          prometheus.Gauge
	GraphEdges          prometheus.Gauge
	GraphPrunedTotal    prometheus.Counter
	TraceUpsertDuration prometheus.Histogram
	TraceStoreRecords   prometheus.Gauge
	TraceStoreSpans     prometheus.Gauge
	TraceStoreCohorts   prometheus.Gauge
	TraceStorePruned    prometheus.Counter

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

	CausalRunsTotal   prometheus.Counter
	CausalRunDuration prometheus.Histogram
	CausalRunFailures prometheus.Counter
	CausalClaimsTotal *prometheus.CounterVec // labels: type, tier

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
		Help:    "Full Events handler latency.",
		Buckets: defaultBuckets,
	})
	m.MergeLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_merge_latency_seconds",
		Help:    "Build + Merge time.",
		Buckets: defaultBuckets,
	})
	m.EventsAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_events_accepted_total",
		Help: "Events merged into graph.",
	})
	m.EventsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_events_rejected_total",
		Help: "Dropped events.",
	}, []string{"reason"})
	m.EventlogFails = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_eventlog_write_failures_total",
		Help: "Failed eventlog writes.",
	})

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
	m.SnapshotLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_snapshot_last_success_timestamp",
		Help: "Unix epoch of last successful save.",
	})
	m.SnapshotLastError = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_snapshot_last_error_timestamp",
		Help: "Unix epoch of last failed save.",
	})
	m.GraphNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_graph_nodes",
		Help: "Current node count.",
	})
	m.GraphEdges = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_graph_edges",
		Help: "Current edge count.",
	})
	m.GraphPrunedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_graph_pruned_total",
		Help: "Number of retention prune cycles executed.",
	})
	m.TraceUpsertDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_trace_upsert_duration_seconds",
		Help:    "Trace store upsert time.",
		Buckets: defaultBuckets,
	})
	m.TraceStoreRecords = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_trace_store_records",
		Help: "Current trace record count.",
	})
	m.TraceStoreSpans = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_trace_store_spans",
		Help: "Current total span count in trace store.",
	})
	m.TraceStoreCohorts = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_trace_store_cohorts",
		Help: "Current trace-store time cohort count.",
	})
	m.TraceStorePruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_trace_store_pruned_total",
		Help: "Total trace records pruned from the trace store.",
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

	m.CausalRunsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_causal_runs_total",
		Help: "Total causal inference runs.",
	})
	m.CausalRunDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "waylog_causal_run_duration_seconds",
		Help:    "Duration of causal inference runs.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
	})
	m.CausalRunFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "waylog_causal_run_failures_total",
		Help: "Total failed causal inference runs.",
	})
	m.CausalClaimsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_causal_claims_total",
		Help: "Total causal claims produced.",
	}, []string{"type", "tier"})

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
		m.IngestLatency, m.MergeLatency,
		m.EventsAccepted, m.EventsRejected, m.EventlogFails,
		m.ReplayLagSeconds, m.ReplayInProgress, m.ReplayFailuresTotal, m.Ready,
		m.InFlightRequests,
		m.SnapshotLastSuccess, m.SnapshotLastError,
		m.GraphNodes, m.GraphEdges, m.GraphPrunedTotal,
		m.TraceUpsertDuration, m.TraceStoreRecords, m.TraceStoreSpans, m.TraceStoreCohorts, m.TraceStorePruned,
		m.AskRequestsTotal, m.AskDuration,
		m.AskToolCallsTotal, m.AskToolDuration,
		m.ToolDirectCallsTotal, m.DedupReplayTotal, m.DedupCacheSize,
		m.ColdEventsWritten, m.ColdEventsDropped, m.ColdBatchLatency,
		m.DeployUpsertsTotal, m.DeployUpsertErrors,
		m.CausalRunsTotal, m.CausalRunDuration, m.CausalRunFailures, m.CausalClaimsTotal,
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
