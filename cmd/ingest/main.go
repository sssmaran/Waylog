package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/alerts"
	"github.com/sssmaran/WaylogCLI/internal/auth"
	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/dashboard"
	"github.com/sssmaran/WaylogCLI/internal/detect"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	eventlogv2 "github.com/sssmaran/WaylogCLI/internal/eventlog/v2"
	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/notify"
	otelhttp "github.com/sssmaran/WaylogCLI/internal/otel"
	"github.com/sssmaran/WaylogCLI/internal/ratelimit"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/triage"
	"github.com/sssmaran/WaylogCLI/internal/triagehttp"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

func main() {
	level := parseSlogLevel(config.Getenv("LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	addr := config.Getenv("INGEST_ADDR", ":8080")

	// ---------------- Hot-window config ----------------

	tickEvery := config.GetenvInt("PRUNE_TICK_SEC", 5)
	graphHotWindow := config.GetenvDuration("GRAPH_HOT_WINDOW", 24*time.Hour)
	if graphHotWindow <= 0 {
		slog.Error("GRAPH_HOT_WINDOW must be positive", "value", graphHotWindow)
		os.Exit(1)
	}
	mcpStdio := config.GetenvBool("MCP_STDIO", false)

	authCfg, err := auth.ParseConfig(map[string]string{
		"WAYLOG_API_KEY":           os.Getenv("WAYLOG_API_KEY"),
		"WAYLOG_WRITE_KEY":         os.Getenv("WAYLOG_WRITE_KEY"),
		"WAYLOG_READ_KEY":          os.Getenv("WAYLOG_READ_KEY"),
		"WAYLOG_AGENT_KEY":         os.Getenv("WAYLOG_AGENT_KEY"),
		"DASHBOARD_AUTH":           os.Getenv("DASHBOARD_AUTH"),
		"DASHBOARD_SESSION_SECRET": os.Getenv("DASHBOARD_SESSION_SECRET"),
		"WAYLOG_PROFILE":           os.Getenv("WAYLOG_PROFILE"),
	})
	if err != nil {
		slog.Error("auth config error", "err", err)
		os.Exit(1)
	}
	for _, w := range authCfg.WeakKeyWarnings() {
		slog.Warn("insecure auth configuration", "detail", w)
	}

	var sm *auth.SessionManager
	if authCfg.DashboardMode != "off" {
		sm = auth.NewSessionManager(authCfg.SessionSecret, auth.DefaultSessionMaxAge)
		sm.Secure = authCfg.Profile == auth.ProfileProd
	}
	sessionCheck := auth.SessionCheckFunc(sm)

	agentKey := ""
	if len(authCfg.AgentKeys) > 0 {
		agentKey = authCfg.AgentKeys[0]
	}

	maxBody := int64(config.GetenvInt("MAX_BODY_BYTES", 1<<20))
	eventLogDir := config.Getenv("EVENT_LOG_DIR", "")
	sqlitePath := config.Getenv("SQLITE_PATH", "")
	askMaxStepsDefault := config.GetenvInt("ASK_MAX_STEPS_DEFAULT", 5)
	askMaxStepsMax := config.GetenvInt("ASK_MAX_STEPS_MAX", 8)
	dashboardRefreshSec := config.GetenvInt("DASHBOARD_REFRESH_SEC", 10)
	prometheusURL := config.Getenv("PROMETHEUS_URL", "")
	grafanaURL := config.Getenv("GRAFANA_URL", "")
	otlpEnabled := config.GetenvBool("OTLP_ENABLED", true)
	otlpGRPCAddr := config.Getenv("OTLP_GRPC_ADDR", ":4317")
	if authCfg.Profile == auth.ProfileProd && otlpEnabled && len(authCfg.WriteKeys) == 0 {
		slog.Error("WAYLOG_PROFILE=prod with OTLP enabled requires WAYLOG_WRITE_KEY — refusing to boot with unauthenticated OTLP")
		os.Exit(1)
	}
	signalRetention := config.GetenvDuration("WAYLOG_SIGNAL_RETENTION", 72*time.Hour)
	alertMatchWindow := config.GetenvDuration("ALERT_MATCH_WINDOW", 15*time.Minute)
	if alertMatchWindow <= 0 {
		alertMatchWindow = 15 * time.Minute
	}
	if alertMatchWindow > 24*time.Hour {
		alertMatchWindow = 24 * time.Hour
	}
	incidentsEnabled := config.GetenvBool("WAYLOG_INCIDENTS_ENABLED", true)
	incidentCfg := incidents.Config{
		TickInterval:            config.GetenvDuration("WAYLOG_INCIDENT_TICK_INTERVAL", 30*time.Second),
		Window:                  config.GetenvDuration("WAYLOG_INCIDENT_WINDOW", 10*time.Minute),
		MinCount:                config.GetenvInt("WAYLOG_INCIDENT_MIN_COUNT", 5),
		MinLift:                 config.GetenvFloat("WAYLOG_INCIDENT_MIN_LIFT", 3.0),
		MinRate:                 config.GetenvFloat("WAYLOG_INCIDENT_MIN_RATE", 0),
		ResolveAfter:            config.GetenvDuration("WAYLOG_INCIDENT_RESOLVE_AFTER", 2*time.Minute),
		DeployCorrelationWindow: config.GetenvDuration("WAYLOG_DEPLOY_CORRELATION_WINDOW", 15*time.Minute),
		SampleLimit:             config.GetenvInt("WAYLOG_INCIDENT_SAMPLE_LIMIT", 5),
		// Traffic-anomaly detector (opt-in). Defaults are the single source of
		// truth for the running binary; SurgeFactor=0 disables surge.
		TrafficAnomaly: incidents.TrafficAnomalyConfig{
			Enabled:        config.GetenvBool("WAYLOG_TRAFFIC_ANOMALY_ENABLED", false),
			DropFactor:     config.GetenvFloat("WAYLOG_TRAFFIC_DROP_FACTOR", 0.5),
			SurgeFactor:    config.GetenvFloat("WAYLOG_TRAFFIC_SURGE_FACTOR", 3.0),
			MinVolume:      config.GetenvInt("WAYLOG_TRAFFIC_MIN_VOLUME", 20),
			SustainedTicks: config.GetenvInt("WAYLOG_TRAFFIC_SUSTAINED_TICKS", 2),
		},
		// Latency-anomaly detector (opt-in). Defaults are the single source of
		// truth for the running binary.
		LatencyAnomaly: incidents.LatencyAnomalyConfig{
			Enabled:        config.GetenvBool("WAYLOG_LATENCY_ANOMALY_ENABLED", false),
			Percentile:     config.GetenvInt("WAYLOG_LATENCY_PERCENTILE", 95),
			Factor:         config.GetenvFloat("WAYLOG_LATENCY_FACTOR", 2.0),
			MinRequests:    config.GetenvInt("WAYLOG_LATENCY_MIN_REQUESTS", 50),
			MinMS:          int64(config.GetenvInt("WAYLOG_LATENCY_MIN_MS", 0)),
			SustainedTicks: config.GetenvInt("WAYLOG_LATENCY_SUSTAINED_TICKS", 2),
		},
	}
	if signalRetention <= 0 {
		slog.Error("WAYLOG_SIGNAL_RETENTION must be positive", "value", signalRetention)
		os.Exit(1)
	}

	trustProxy := config.GetenvBool("WAYLOG_TRUST_PROXY", false)
	if _, err := llm.SelectFromEnv(); err != nil {
		slog.Error("LLM provider config error", "err", err)
		os.Exit(1)
	}

	dedupCache := ingest.NewDedupCache()
	planStore := ingest.NewPlanStore()

	reg := tools.NewRegistry()

	// Prometheus metrics
	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg)

	eventLogSync := config.GetenvBool("EVENT_LOG_SYNC", true)
	eventLogMaxMB := int64(config.GetenvInt("EVENT_LOG_MAX_FILE_MB", 50))
	eventLogRetention := config.GetenvDuration("EVENT_LOG_RETENTION", 72*time.Hour)
	if eventLogRetention <= 0 {
		slog.Error("EVENT_LOG_RETENTION must be positive", "value", eventLogRetention)
		os.Exit(1)
	}
	eventLogV2Dir := eventlogv2.ResolveDir(os.Getenv("EVENT_LOG_V2_DIR"), eventLogDir)
	v2Wal, err := eventlogv2.New(eventLogV2Dir,
		eventlogv2.WithSync(eventLogSync),
		eventlogv2.WithMaxBytes(eventLogMaxMB*1024*1024),
	)
	if err != nil {
		slog.Error("eventlog v2 init failed", "err", err)
		os.Exit(1)
	}
	defer v2Wal.Close()

	dedupCapacity := config.GetenvInt("WAYLOG_V2_DEDUP_CAPACITY", ingestv2.DefaultDedupCapacity)
	v2Dedup := ingestv2.NewDedup(dedupCapacity, m.EventDedupCacheSize)
	v2Index := ingestv2.NewRecentIndex(m.V2IndexSize)
	v2Projector := ingestv2.NewProjector(v2Index)
	v2ReplaySince := time.Now().Add(-graphHotWindow)
	v2Replay, err := ingestv2.ReplayWAL(eventLogV2Dir, v2Dedup, v2Projector, v2ReplaySince, m)
	if err != nil {
		slog.Error("eventlog v2 replay failed", "err", err)
		os.Exit(1)
	}
	m.EventDedupReplayLoaded.Add(float64(v2Replay.DedupLoaded))
	m.V2ReplayProjected.Add(float64(v2Replay.Projected))
	slog.Info("eventlog v2 enabled",
		"dir", eventLogV2Dir,
		"sync_per_write", eventLogSync,
		"max_file_mb", eventLogMaxMB,
		"retention", eventLogRetention,
		"dedup_capacity", dedupCapacity,
		"replay_since", v2ReplaySince,
		"dedup_replay_loaded", v2Replay.DedupLoaded,
		"replay_projected", v2Replay.Projected,
		"replay_decode_fails", v2Replay.DecodeFails,
	)

	// Optional SQLite cold store
	var coldDB coldstore.ManagedStore
	var coldWriter *coldstore.BatchWriter
	var signalStore signals.Store = signals.UnavailableStore{}
	if sqlitePath != "" {
		if eventLogDir == "" {
			slog.Warn("SQLITE_PATH set without EVENT_LOG_DIR — cold store is async-only, " +
				"events may be lost on crash. Set EVENT_LOG_DIR for durable writes")
		}
		var err error
		coldDB, err = coldstore.Open(sqlitePath)
		if err != nil {
			slog.Error("coldstore init failed", "err", err)
			os.Exit(1)
		}
		defer coldDB.Close()

		coldWriter = coldstore.NewBatchWriter(coldDB.(*coldstore.SQLiteStore), coldstore.BatchWriterConfig{
			QueueSize:     config.GetenvInt("SQLITE_MAX_QUEUE", 10000),
			BatchSize:     config.GetenvInt("SQLITE_BATCH_SIZE", 100),
			FlushInterval: config.GetenvDuration("SQLITE_FLUSH_INTERVAL", 500*time.Millisecond),
		}, m)
		coldWriter.Start()
		signalStore = coldstore.NewSignalStore(coldDB.(*coldstore.SQLiteStore))

		slog.Info("coldstore enabled", "path", sqlitePath)
	}

	// Create ingest server with the store
	ingestServer := ingest.NewServer(ingest.ServerConfig{
		MaxBodyBytes:             maxBody,
		EventLogDir:              eventLogDir,
		Metrics:                  m,
		StartTime:                time.Now(),
		AskRegistry:              reg,
		AskMaxStepsDefault:       askMaxStepsDefault,
		AskMaxStepsMax:           askMaxStepsMax,
		DashboardRefreshSec:      dashboardRefreshSec,
		PrometheusURL:            prometheusURL,
		GrafanaURL:               grafanaURL,
		DedupCache:               dedupCache,
		AgentKey:                 agentKey,
		TrustProxy:               trustProxy,
		ColdWriter:               coldWriter,
		ColdStore:                coldDB,
		PlanStore:                planStore,
		GraphHotWindow:           graphHotWindow,
		OTLPEnabled:              otlpEnabled,
		OTLPGRPCEnabled:          otlpEnabled && otlpGRPCAddr != "",
		OTLPGRPCAddr:             otlpGRPCAddr,
		IncidentsEnabled:         incidentsEnabled && sqlitePath != "",
		IncidentsPersistent:      incidentsEnabled && sqlitePath != "",
		IncidentRebuildSupported: incidentsEnabled && sqlitePath != "",
		Profile:                  authCfg.Profile,
	})

	// Optional append-only v1 event log
	var el *eventlog.Writer
	if eventLogDir != "" {
		var err error
		el, err = eventlog.NewWithConfig(eventLogDir, eventlog.WriterConfig{
			SyncOnWrite:  eventLogSync,
			MaxFileBytes: eventLogMaxMB * 1024 * 1024,
		})
		if err != nil {
			slog.Error("eventlog init failed", "err", err)
			os.Exit(1)
		}
		defer el.Close()
		ingestServer.EventLog = el
		slog.Info("eventlog enabled",
			"dir", eventLogDir,
			"sync_per_write", eventLogSync,
			"max_file_mb", eventLogMaxMB,
			"retention", eventLogRetention,
		)
	}
	ingestServer.SetReady()

	// ---------------- HTTP server ----------------

	// Start dedup cache eviction
	dedupCtx, dedupCancel := context.WithCancel(context.Background())
	defer dedupCancel()
	dedupCache.StartEviction(dedupCtx)

	corsOrigin := config.Getenv("CORS_ORIGIN", "*")

	// Per-key rate limits run outermost (before auth) so floods of invalid
	// credentials are throttled too. 0 disables; only prod limits by default.
	defWriteRPS, defReadRPS, defAgentRPS := 0, 0, 0
	if authCfg.Profile == auth.ProfileProd {
		defWriteRPS, defReadRPS, defAgentRPS = 1000, 200, 50
	}
	writeLimit := ratelimit.Middleware(ratelimit.New(config.GetenvInt("WAYLOG_RATE_LIMIT_WRITE_RPS", defWriteRPS)), "write", m)
	readLimit := ratelimit.Middleware(ratelimit.New(config.GetenvInt("WAYLOG_RATE_LIMIT_READ_RPS", defReadRPS)), "read", m)
	agentLimit := ratelimit.Middleware(ratelimit.New(config.GetenvInt("WAYLOG_RATE_LIMIT_AGENT_RPS", defAgentRPS)), "agent", m)

	writeKeyAuth := auth.Middleware("write", authCfg.WriteKeys, nil)
	readKeyAuth := auth.Middleware("read", authCfg.ReadKeys, sessionCheck)
	agentKeyAuth := auth.Middleware("agent", authCfg.AgentKeys, nil)
	writeAuth := func(h http.Handler) http.Handler { return writeLimit(writeKeyAuth(h)) }
	readAuth := func(h http.Handler) http.Handler { return readLimit(readKeyAuth(h)) }
	agentAuth := func(h http.Handler) http.Handler { return agentLimit(agentKeyAuth(h)) }
	dashGate := auth.DashboardGate(authCfg, sm)

	mux := http.NewServeMux()

	// Operational probes — always open.
	mux.HandleFunc("/healthz", ingestServer.Health)
	mux.HandleFunc("/livez", ingestServer.Livez)
	mux.HandleFunc("/readyz", ingestServer.Readyz)
	mux.Handle("/metrics", m.Handler())

	// Write endpoints.
	eventsV2, err := ingestv2.New(ingestv2.Config{
		Metrics: m,
		Dedup:   v2Dedup,
		WAL:     v2Wal,
		Index:   v2Index,
		Project: v2Projector,
	})
	if err != nil {
		slog.Error("initialize v2 ingest handler", "err", err)
		os.Exit(1)
	}
	mux.Handle("/v1/events", writeAuth(http.HandlerFunc(eventsV2.Events)))
	mux.Handle("/v1/events/validate", writeAuth(http.HandlerFunc(eventsV2.Validate)))
	signalHandler := signals.NewHandler(signalStore, m)
	mux.Handle("/v1/signals", writeAuth(http.HandlerFunc(signalHandler.Signals)))

	// OTLP/HTTP traces reuse the same schema-2.0 WAL and projector as the SDK path.
	var otlpGRPCServer *grpc.Server
	if otlpEnabled {
		// A service.version change observed on OTLP spans auto-registers a
		// deployment so deploy correlation works without the deploy webhook.
		var deployTracker *otelhttp.DeployTracker
		if sqlite, ok := coldDB.(*coldstore.SQLiteStore); ok {
			deployTracker = otelhttp.NewDeployTracker(sqlite)
		}
		otlpHandler := otelhttp.NewHandler(eventsV2, m, maxBody, deployTracker)
		mux.Handle("/v1/otlp/v1/traces", writeAuth(http.HandlerFunc(otlpHandler.ServeHTTP)))
		slog.Info("otlp enabled", "endpoint", "/v1/otlp/v1/traces")
		if otlpGRPCAddr != "" {
			otlpGRPCServer = grpc.NewServer(
				grpc.UnaryInterceptor(otelhttp.AuthUnaryInterceptor(authCfg.WriteKeys)),
				grpc.MaxRecvMsgSize(int(maxBody)),
			)
			coltracepb.RegisterTraceServiceServer(otlpGRPCServer, otelhttp.NewTraceServiceServer(eventsV2, m, maxBody, deployTracker))
			ingestServer.SetOTLPGRPC(true, otlpGRPCAddr)
		}
	}

	// Read endpoints — CORS outermost so OPTIONS preflight passes without auth.
	readCORS := func(h http.HandlerFunc) http.Handler {
		inner := readAuth(http.HandlerFunc(h))
		return http.HandlerFunc(ingest.CORSWrap(corsOrigin, "GET, OPTIONS",
			func(w http.ResponseWriter, r *http.Request) { inner.ServeHTTP(w, r) }))
	}
	var incidentEngine *incidents.Engine
	incidentRunning := false
	v2Reader := ingestv2.NewReader(v2Index)
	v2ReadHandler := ingestv2.NewReadHandler(v2Reader, m, graphHotWindow)
	mux.Handle("/v1/events/search", readCORS(v2ReadHandler.EventSearch))
	mux.Handle("/v1/errors", readCORS(v2ReadHandler.Errors))
	mux.Handle("/v1/blast_radius", readCORS(v2ReadHandler.BlastRadius))
	mux.Handle("/v1/traces/story", readCORS(v2ReadHandler.TraceStory))
	mux.Handle("/v1/traces/recent", readCORS(v2ReadHandler.RecentTraces))
	// ServeMux chooses the longest matching pattern, so these prefix handlers
	// do not capture the concrete routes above or /v1/events/validate.
	mux.Handle("/v1/events/", readCORS(v2ReadHandler.EventByID))
	mux.Handle("/v1/traces/", readCORS(v2ReadHandler.TraceByID))
	// Register the reader-backed explain_request + blast_radius. The
	// triage_incident + render_triage_report pair registers later once
	// triage.Engine exists.
	{
		v2ToolReader := incidentReaderAdapter{reader: v2Reader}
		if err := tools.RegisterExplainRequestTool(reg, v2ToolReader); err != nil {
			slog.Error("register explain_request v2", "err", err)
			os.Exit(1)
		}
		if err := tools.RegisterBlastRadiusTool(reg, v2ToolReader); err != nil {
			slog.Error("register blast_radius v2", "err", err)
			os.Exit(1)
		}
	}
	if incidentsEnabled {
		if sqlite, ok := coldDB.(*coldstore.SQLiteStore); ok {
			incidentStore := coldstore.NewIncidentStore(sqlite)
			incReader := incidentReaderAdapter{reader: v2Reader}
			incidentEngine = incidents.NewEngine(
				incReader,
				signalStore,
				coldDeployAdapter{store: sqlite},
				incidentStore,
				incidentCfg,
				m,
				slog.Default(),
			)
			if err := incidentEngine.Bootstrap(context.Background()); err != nil {
				slog.Error("incident engine bootstrap failed", "err", err)
				os.Exit(1)
			}
			// Outbound incident notification (opt-in: enabled only when a
			// destination is configured). Best-effort, fires once on open/resolve.
			notifyCfg := notify.Config{
				SlackWebhookURL:     config.Getenv("WAYLOG_NOTIFY_SLACK_WEBHOOK", ""),
				PagerDutyRoutingKey: config.Getenv("WAYLOG_NOTIFY_PAGERDUTY_ROUTING_KEY", ""),
				GenericWebhookURL:   config.Getenv("WAYLOG_NOTIFY_WEBHOOK_URL", ""),
				BaseURL:             config.Getenv("WAYLOG_NOTIFY_BASE_URL", ""),
			}
			if notifyCfg.Enabled() {
				incidentEngine.SetNotifier(notify.New(notifyCfg, slog.Default()))
				slog.Info("outbound incident notification enabled",
					"slack", notifyCfg.SlackWebhookURL != "",
					"pagerduty", notifyCfg.PagerDutyRoutingKey != "",
					"webhook", notifyCfg.GenericWebhookURL != "")
			}
			if config.GetenvBool("WAYLOG_REBUILD_INCIDENTS_ON_START", false) {
				rebuildMaxEvents := config.GetenvInt("WAYLOG_INCIDENT_REBUILD_MAX_EVENTS", 250000)
				if rebuildMaxEvents <= 0 {
					rebuildMaxEvents = 250000
				}
				replayWindow := graphHotWindow
				// 4× window: the spike baseline is the median of the 3 prior
				// windows, so rebuild needs current + 3 baselines of history.
				if minWindow := 4 * incidentCfg.Window; minWindow > replayWindow {
					replayWindow = minWindow
				}
				replaySince := time.Now().UTC().Add(-replayWindow)
				seed := incidentEngine.SnapshotActive()
				for _, inc := range seed {
					if inc.StartedAt.Before(replaySince) {
						slog.Info("incident continuity broken: started_at older than WAL retention",
							"incident_id", inc.IncidentID,
							"started_at", inc.StartedAt,
							"replay_since", replaySince,
						)
						break
					}
				}
				tempIndex := ingestv2.NewRecentIndex(nil)
				tempDedup := ingestv2.NewDedup(dedupCapacity, nil)
				tempProjector := ingestv2.NewProjector(tempIndex)
				replay, err := ingestv2.ReplayWAL(eventLogV2Dir, tempDedup, tempProjector, replaySince, m)
				if err != nil {
					m.IncidentRebuildFailures.Inc()
					slog.Error("incident rebuild WAL replay failed", "err", err)
					os.Exit(1)
				}
				m.IncidentRebuildReplayed.Add(float64(replay.Projected))
				if replay.Projected > rebuildMaxEvents {
					m.IncidentRebuildFailures.Inc()
					slog.Error("incident rebuild replay exceeded max events", "projected", replay.Projected, "max_events", rebuildMaxEvents)
					os.Exit(1)
				}
				if replay.Projected == 0 {
					// Empty WAL replay while rebuild was explicitly requested.
					// Transition only the seed rows whose StartedAt precedes
					// replaySince — those are stale beyond the hot window and
					// their continuing "active" status is no longer evidence-
					// backed. Non-stale active rows in the same seed are left
					// untouched and will be re-evaluated by the next live tick.
					staleTransitioned := 0
					if len(seed) > 0 {
						incidentStoreRef := incidentStore
						now := time.Now().UTC()
						for _, inc := range seed {
							if inc.Status != incidents.StatusActive {
								continue
							}
							if !inc.StartedAt.Before(replaySince) {
								continue
							}
							row := inc
							row.Status = incidents.StatusRecovering
							t := now
							row.RecoveringAt = &t
							row.UpdatedAt = now
							if err := incidentStoreRef.Upsert(context.Background(), row); err != nil {
								slog.Warn("stale-active rebuild transition failed",
									"incident_id", row.IncidentID, "err", err)
								continue
							}
							staleTransitioned++
						}
						if staleTransitioned > 0 {
							if err := incidentEngine.Bootstrap(context.Background()); err != nil {
								slog.Error("incident engine re-bootstrap after stale transition failed", "err", err)
								os.Exit(1)
							}
							slog.Info("incidents rebuild: stale active rows transitioned to recovering",
								"transitioned", staleTransitioned,
								"replay_since", replaySince)
						} else {
							slog.Warn("incidents rebuild skipped: WAL replay returned no events; preserving SQLite as-is")
						}
					}
				} else {
					result, err := incidents.Rebuild(context.Background(), incidents.RebuildDeps{
						Engine: incidentEngine,
						Reader: incidentReaderAdapter{reader: ingestv2.NewReader(tempIndex)},
						Now:    time.Now,
					})
					if err != nil {
						m.IncidentRebuildFailures.Inc()
						slog.Error("incident rebuild failed", "err", err)
						os.Exit(1)
					}
					m.IncidentRebuildDuration.Observe(result.Duration.Seconds())
					m.IncidentRebuildRows.Add(float64(result.RowsReplaced))
					slog.Info("incident rebuild complete",
						"replayed_events", replay.Projected,
						"rows_replaced", result.RowsReplaced,
						"duration", result.Duration,
					)
				}
			}
			incidentHandler := incidents.NewHandler(incidentEngine)
			mux.Handle("/v1/incidents/active", readCORS(incidentHandler.Active))
			mux.Handle("/v1/incidents/", readCORS(incidentHandler.Incident))
			ingestServer.SetDetector(incidentInsightAdapter{engine: incidentEngine})

			// Triage engine: deterministic TriageReport build for a given
			// incident. Wires the incidents engine for lookups, the v2
			// reader for blast queries and TraceStoryByTraceID, and the
			// signal store + alert adapter for context. Read-scope auth.
			triageEng, err := triage.NewEngine(triage.Deps{
				Incidents: triage.NewIncidentLookupAdapter(incidentEngine),
				Blast:     triage.NewBlastQueryAdapter(incReader),
				Story: triage.NewStoryBuilderAdapter(
					incidentEngine,
					func(traceID string) (apiv2.StoryResponse, bool) {
						return v2Reader.TraceStoryByTraceID(traceID)
					},
				),
				Signals:    triage.NewSignalQueryAdapter(signalStore),
				Alerts:     triage.NewAlertQueryAdapter(signalStore, alertMatchWindow),
				NextChecks: triage.NewNextChecksAdapter(),
				Deploy:     triage.NewSuspectChangeAdapter(coldDeployAdapter{store: sqlite}),
			})
			if err != nil {
				slog.Error("triage engine init failed", "err", err)
				os.Exit(1)
			}
			if err := tools.RegisterTriageTool(reg, triageEng); err != nil {
				slog.Error("triage tool register failed", "err", err)
				os.Exit(1)
			}
			if err := tools.RegisterTriageReportTool(reg, triageEng); err != nil {
				slog.Error("triage report tool register failed", "err", err)
				os.Exit(1)
			}
			if err := tools.RegisterSuspectChangeTool(reg, triageEng); err != nil {
				slog.Error("suspect_change tool register failed", "err", err)
				os.Exit(1)
			}
			if err := tools.RegisterListActiveIncidentsTool(reg, incidentEngine); err != nil {
				slog.Error("list_active_incidents tool register failed", "err", err)
				os.Exit(1)
			}
			triageHandler := triagehttp.NewHandler(triageEng)
			mux.Handle("/v1/triage/", readCORS(triageHandler.Triage))

			incidentRunning = true
			slog.Info("incident engine enabled", "interval", incidentCfg.TickInterval, "window", incidentCfg.Window)
		} else {
			slog.Warn("incidents requested but SQLite not configured; running without incidents")
		}
	}
	mux.Handle("/v1/capabilities", readCORS(ingestServer.Capabilities))
	mux.Handle("/v1/insight", readCORS(ingestServer.Insight))
	alertHandler := alerts.NewHandler(signalStore, incidentEngine, v2Reader, alertMatchWindow)
	mux.Handle("/v1/alerts", writeAuth(http.HandlerFunc(alertHandler.Alerts)))

	// Deployments — dual method: GET=read, POST=write.
	mux.Handle("/v1/deployments", http.HandlerFunc(
		ingest.CORSWrap(corsOrigin, "GET, POST, OPTIONS", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				readAuth(http.HandlerFunc(ingestServer.Deployments)).ServeHTTP(w, r)
			case http.MethodPost:
				writeAuth(http.HandlerFunc(ingestServer.DeployWebhook)).ServeHTTP(w, r)
			case http.MethodOptions:
				w.WriteHeader(http.StatusNoContent)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		}),
	))

	// Agent endpoints.
	agentCORS := func(methods string, h http.HandlerFunc) http.Handler {
		inner := agentAuth(http.HandlerFunc(h))
		return http.HandlerFunc(ingest.CORSWrap(corsOrigin, methods,
			func(w http.ResponseWriter, r *http.Request) { inner.ServeHTTP(w, r) }))
	}
	mux.Handle("/v1/tools", agentCORS("GET, OPTIONS", ingestServer.Tools))
	mux.Handle("/v1/tools/", agentCORS("POST, OPTIONS", ingestServer.ToolCall))
	mux.Handle("/v1/ask", agentCORS("POST, OPTIONS", ingestServer.Ask))
	mux.Handle("/v1/plans/execute", agentCORS("POST, OPTIONS", ingestServer.PlanExecute))
	mux.Handle("/v1/stream/plans/", agentCORS("GET, OPTIONS", ingestServer.PlanStream))

	// Dashboard.
	mux.Handle("/ui/", dashGate(http.StripPrefix("/ui/", dashboard.Handler())))
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})

	handler := ingest.CorrelationIDMiddleware(mux)

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: config.GetenvDuration("READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       config.GetenvDuration("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      config.GetenvDuration("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:       config.GetenvDuration("IDLE_TIMEOUT", 120*time.Second),
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if _, ok := signalStore.(*coldstore.SignalStore); ok {
		go signals.RunRetention(ctx, signalStore, signalRetention, 5*time.Minute, m, slog.Default())
	}
	if incidentRunning {
		go incidentEngine.Run(ctx)
		if sqlite, ok := coldDB.(*coldstore.SQLiteStore); ok {
			incidentRetention := config.GetenvDuration("WAYLOG_INCIDENT_RETENTION", 168*time.Hour)
			go incidents.RunRetention(ctx, coldstore.NewIncidentStore(sqlite), incidentRetention, 5*time.Minute, m, slog.Default())
		}
	}

	go func() {
		slog.Info("ingest listening", "addr", addr, "graph_hot_window", graphHotWindow)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("ingest server error", "err", err)
			os.Exit(1)
		}
	}()

	if otlpGRPCServer != nil {
		lis, err := net.Listen("tcp", otlpGRPCAddr)
		if err != nil {
			slog.Error("otlp grpc listen failed", "addr", otlpGRPCAddr, "err", err)
			os.Exit(1)
		}
		go func() {
			slog.Info("otlp grpc enabled", "addr", otlpGRPCAddr)
			if err := otlpGRPCServer.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				slog.Error("otlp grpc server error", "err", err)
				os.Exit(1)
			}
		}()
	}

	// ---------------- Embedded CLI ----------------

	if mcpStdio {
		go func() {
			slog.Info("MCP stdio ready", "protocol", "2024-11-05")
			if err := stdio.Serve(ctx, os.Stdin, os.Stdout, reg, stdio.ServerInfo{
				Name:    "waylog",
				Version: "0.1.0",
			}); err != nil && err != context.Canceled {
				slog.Error("mcp stdio error", "err", err)
			}
		}()
	} else {
		go replLoop()
	}

	// ---------------- Periodic v2-index pruning ----------------

	pruneTicker := time.NewTicker(time.Duration(tickEvery) * time.Second)
	defer pruneTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-pruneTicker.C:
				cutoff := time.Now().Add(-graphHotWindow)
				v2Pruned := v2Index.PruneOlderThan(cutoff)
				if v2Pruned.Events > 0 {
					m.V2IndexPruned.Add(float64(v2Pruned.Events))
				}
			}
		}
	}()

	// ---------------- Event log retention ----------------

	if el != nil || v2Wal != nil {
		go func() {
			retTicker := time.NewTicker(5 * time.Minute)
			defer retTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-retTicker.C:
					if el != nil {
						n, err := eventlog.PruneOlderThan(eventLogDir, eventLogRetention, el.ActivePath())
						if err != nil {
							slog.Warn("eventlog retention cleanup error", "err", err)
						} else if n > 0 {
							slog.Info("eventlog retention cleanup", "dir", eventLogDir, "deleted", n)
						}
					}
					if v2Wal != nil {
						n, err := eventlog.PruneOlderThan(eventLogV2Dir, eventLogRetention, v2Wal.ActivePath())
						if err != nil {
							slog.Warn("eventlog v2 retention cleanup error", "err", err)
						} else if n > 0 {
							slog.Info("eventlog v2 retention cleanup", "dir", eventLogV2Dir, "deleted", n)
						}
					}
				}
			}
		}()
	}

	// ---------------- Anomaly detection ticker ----------------

	// ---------------- Shutdown ----------------

	<-ctx.Done()
	slog.Info("ingest shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("ingest graceful shutdown failed", "err", err)
	} else {
		slog.Info("ingest shutdown complete")
	}
	if otlpGRPCServer != nil {
		done := make(chan struct{})
		go func() {
			otlpGRPCServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
			slog.Info("otlp grpc shutdown complete")
		case <-time.After(5 * time.Second):
			otlpGRPCServer.Stop()
			slog.Warn("otlp grpc graceful shutdown timed out; forced stop")
		}
	}

	planStore.Close()

	if coldWriter != nil {
		coldWriter.Stop()
		slog.Info("coldstore writer drained")
	}
}

func replLoop() {
	config.LoadDotEnv(".env")

	in := bufio.NewScanner(os.Stdin)

	printHelp()
	for {
		os.Stdout.WriteString("\033[1m\033[36mingest>\033[0m ")

		if !in.Scan() {
			return
		}

		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}

		switch line {
		case "exit", "quit":
			os.Stdout.WriteString("bye\n")
			os.Exit(0)
		case "help":
			printHelp()
			continue
		}

		args := strings.Fields(line)
		cli.Run(args)
	}
}

func printHelp() {
	os.Stdout.WriteString("\033[1m\033[36mcommands:\033[0m\n")
	os.Stdout.WriteString("  waylog \"<question>\"\n")
	os.Stdout.WriteString("  help\n")
	os.Stdout.WriteString("  exit\n")
	os.Stdout.WriteString("\n\033[1m\033[36mexamples:\033[0m\n")
	os.Stdout.WriteString("  waylog \"\033[33mshow top errors\033[0m\"\n")
	os.Stdout.WriteString("  waylog \"\033[33msummarize trace <trace-id>\033[0m\"\n")
	os.Stdout.WriteString("  waylog \"\033[33mexplain request <request-id>\033[0m\"\n")
	os.Stdout.WriteString("\n\033[2mnotes: MCP stdio: run with MCP_STDIO=1\033[0m\n")
}

type coldDeployAdapter struct {
	store *coldstore.SQLiteStore
}

func (a coldDeployAdapter) DeploymentsInWindow(ctx context.Context, start, end time.Time, serviceFilter string) ([]incidents.Deployment, error) {
	rows, err := a.store.DeploymentsInWindow(ctx, start, end, serviceFilter)
	if err != nil {
		return nil, err
	}
	out := make([]incidents.Deployment, 0, len(rows))
	for _, row := range rows {
		out = append(out, incidents.Deployment{
			ID:           row.ID,
			Service:      row.Service,
			Version:      row.Version,
			Env:          row.Env,
			FirstSeen:    row.FirstSeen,
			LastSeen:     row.LastSeen,
			Metadata:     row.Metadata,
			CommitSHA:    row.CommitSHA,
			PRURL:        row.PRURL,
			CommitAuthor: row.CommitAuthor,
		})
	}
	return out, nil
}

// DeploymentByID satisfies triage.DeployStore: hydrates suspect-change provenance.
func (a coldDeployAdapter) DeploymentByID(ctx context.Context, id string) (*triage.DeployRecord, error) {
	d, err := a.store.DeploymentByID(ctx, id)
	if err != nil || d == nil {
		return nil, err
	}
	return &triage.DeployRecord{
		ID:           d.ID,
		Service:      d.Service,
		Version:      d.Version,
		CommitSHA:    d.CommitSHA,
		PRURL:        d.PRURL,
		CommitAuthor: d.CommitAuthor,
		FirstSeen:    d.FirstSeen,
	}, nil
}

// DeployErrorRate satisfies triage.DeployStore via the shared rate-delta helper.
func (a coldDeployAdapter) DeployErrorRate(ctx context.Context, service string, firstSeen time.Time) (*float64, *float64, error) {
	delta, err := a.store.DeployErrorRateDelta(ctx, service, firstSeen)
	if err != nil {
		return nil, nil, err
	}
	return delta.BeforeRate, delta.AfterRate, nil
}

type incidentReaderAdapter struct {
	reader *ingestv2.Reader
}

func (a incidentReaderAdapter) Errors(f incidents.SearchFilter, limit int) incidents.ErrorsResult {
	res := a.reader.Errors(toV2SearchFilter(f), nil, limit)
	return incidents.ErrorsResult{Rows: res.Rows}
}

func (a incidentReaderAdapter) BlastRadius(f incidents.SearchFilter, key apiv2.BlastKey) apiv2.BlastRadiusResponse {
	return a.reader.BlastRadius(toV2SearchFilter(f), ingestv2.BlastKeyMode{Key: key})
}

func (a incidentReaderAdapter) SearchEvents(f incidents.SearchFilter, limit int) []*eventv2.Event {
	res := a.reader.SearchEvents(toV2SearchFilter(f), nil, limit)
	return res.Events
}

func (a incidentReaderAdapter) ServiceStats(f incidents.SearchFilter, percentile, limit int) []incidents.ServiceStatsRow {
	rows := a.reader.ServiceStats(toV2SearchFilter(f), percentile, limit)
	out := make([]incidents.ServiceStatsRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, incidents.ServiceStatsRow{Service: r.Service, Count: r.Count, LatencyMS: r.LatencyMS})
	}
	return out
}

func (a incidentReaderAdapter) TraceStoryByTraceID(traceID string) (apiv2.StoryResponse, bool) {
	return a.reader.TraceStoryByTraceID(traceID)
}

func (a incidentReaderAdapter) TraceEvents(traceID string) ([]*eventv2.Event, bool) {
	result, ok := a.reader.GetTrace(traceID)
	if !ok {
		return nil, false
	}
	return result.Events, true
}

func toV2SearchFilter(f incidents.SearchFilter) ingestv2.SearchFilter {
	return ingestv2.SearchFilter{
		Service:   f.Service,
		Statuses:  f.Statuses,
		ErrorCode: f.ErrorCode,
		Since:     f.Since,
		Until:     f.Until,
	}
}

type incidentInsightAdapter struct {
	engine *incidents.Engine
}

func (a incidentInsightAdapter) Current() *detect.Insight {
	if a.engine == nil {
		return nil
	}
	inc, err := a.engine.TopActive(context.Background())
	if err != nil || inc == nil {
		return nil
	}
	return projectIncidentInsight(*inc, time.Now().UTC())
}

func projectIncidentInsight(inc incidents.Incident, detectedAt time.Time) *detect.Insight {
	affectedUsers := 0
	if inc.AffectedUsers != nil {
		affectedUsers = *inc.AffectedUsers
	}
	out := &detect.Insight{
		DetectedAt:       detectedAt,
		TopErrorCode:     inc.ErrorFamily.ErrorCode,
		Lift:             inc.Lift,
		CurrentCount:     inc.CurrentCount,
		BaselineCount:    inc.BaselineCount,
		AffectedRequests: inc.AffectedRequests,
		AffectedUsers:    affectedUsers,
		Services:         append([]string(nil), inc.TopServices...),
		SeverityScore:    float64(inc.Severity),
	}
	if len(out.Services) == 0 {
		out.Services = []string{inc.Service}
	}
	for _, ev := range inc.Evidence {
		if ev.Kind == incidents.EvidenceDeployment && ev.DeployID != "" {
			out.DeployCorrelation = &detect.DeployCorrelation{
				DeploymentID: ev.DeployID,
				Service:      ev.Service,
				Confidence:   incidentConfidenceFloat(inc.Confidence),
			}
			break
		}
	}
	return out
}

func incidentConfidenceFloat(c incidents.Confidence) float64 {
	switch c {
	case incidents.ConfidenceHigh:
		return 0.9
	case incidents.ConfidenceMedium:
		return 0.65
	default:
		return 0.35
	}
}

func parseSlogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
