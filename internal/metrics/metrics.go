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
	InFlightRequests   prometheus.Gauge
	SnapshotLastSuccess prometheus.Gauge
	SnapshotLastError   prometheus.Gauge
	GraphNodes       prometheus.Gauge
	GraphEdges       prometheus.Gauge
	GraphPrunedTotal prometheus.Counter
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

	reg.MustRegister(
		m.IngestLatency, m.MergeLatency,
		m.EventsAccepted, m.EventsRejected, m.EventlogFails,
		m.ReplayLagSeconds, m.ReplayInProgress, m.ReplayFailuresTotal, m.Ready,
		m.InFlightRequests,
		m.SnapshotLastSuccess, m.SnapshotLastError,
		m.GraphNodes, m.GraphEdges, m.GraphPrunedTotal,
	)

	return m
}

// Handler returns an http.Handler that serves Prometheus metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
