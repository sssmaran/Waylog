package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var defaultBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

type Metrics struct {
	reg *prometheus.Registry

	ActiveSessions    *prometheus.GaugeVec
	ActiveRuns        prometheus.Gauge
	RunsTotal         *prometheus.CounterVec
	SessionsTotal     *prometheus.CounterVec
	StepsTotal        *prometheus.CounterVec
	TokensTotal       *prometheus.CounterVec
	ToolCallsTotal    *prometheus.CounterVec
	RunDuration       prometheus.Histogram
	SessionDuration   *prometheus.HistogramVec
	StepDuration      *prometheus.HistogramVec
	SessionSteps      *prometheus.HistogramVec
	RunSessions       prometheus.Histogram
	IngestEventsTotal *prometheus.CounterVec
	IngestDuration    prometheus.Histogram
}

func New(reg *prometheus.Registry) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{reg: reg}

	m.ActiveSessions = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "waylog_agent_active_sessions", Help: "Currently active agent sessions",
	}, []string{"agent_name"})
	m.ActiveRuns = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "waylog_agent_active_runs", Help: "Currently active agent runs",
	})
	m.RunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_runs_total", Help: "Terminal run transitions",
	}, []string{"status"})
	m.SessionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_sessions_total", Help: "Terminal session transitions",
	}, []string{"agent_name", "status"})
	m.StepsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_steps_total", Help: "Total completed steps",
	}, []string{"agent_name", "model"})
	m.TokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_tokens_total", Help: "Total tokens",
	}, []string{"agent_name", "model", "direction"})
	m.ToolCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_tool_calls_total", Help: "Tool call count",
	}, []string{"agent_name", "tool_name", "status"})
	m.RunDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "waylog_agent_run_duration_seconds", Help: "Run wall-clock duration",
		Buckets: defaultBuckets,
	})
	m.SessionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "waylog_agent_session_duration_seconds", Help: "Session wall-clock duration",
		Buckets: defaultBuckets,
	}, []string{"agent_name"})
	m.StepDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "waylog_agent_step_duration_seconds", Help: "Step latency",
		Buckets: defaultBuckets,
	}, []string{"agent_name", "model"})
	m.SessionSteps = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "waylog_agent_session_steps", Help: "Steps per session",
		Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 34},
	}, []string{"agent_name"})
	m.RunSessions = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "waylog_agent_run_sessions", Help: "Sessions per run",
		Buckets: []float64{1, 2, 3, 5, 8, 13},
	})
	m.IngestEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "waylog_agent_ingest_events_total", Help: "Ingest event outcomes",
	}, []string{"status"})
	m.IngestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "waylog_agent_ingest_duration_seconds", Help: "Batch ingest latency",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
	})

	reg.MustRegister(
		m.ActiveSessions, m.ActiveRuns, m.RunsTotal, m.SessionsTotal,
		m.StepsTotal, m.TokensTotal, m.ToolCallsTotal,
		m.RunDuration, m.SessionDuration, m.StepDuration,
		m.SessionSteps, m.RunSessions,
		m.IngestEventsTotal, m.IngestDuration,
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
