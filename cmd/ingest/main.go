package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/auth"
	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/dashboard"
	"github.com/sssmaran/WaylogCLI/internal/detect"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	eventlogv2 "github.com/sssmaran/WaylogCLI/internal/eventlog/v2"
	"github.com/sssmaran/WaylogCLI/internal/graph/causal"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	otelhttp "github.com/sssmaran/WaylogCLI/internal/otel"
	"github.com/sssmaran/WaylogCLI/internal/persist"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

var graphStore *graphstore.Store

func main() {
	level := parseSlogLevel(config.Getenv("LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	addr := config.Getenv("INGEST_ADDR", ":8080")

	// ---------------- Graph persistence config ----------------

	snapshotPath := config.Getenv("SNAPSHOT_PATH", "./data/graph_snapshot.json")
	snapshotEvery := config.GetenvInt("SNAPSHOT_EVERY_SEC", 5)
	snapshotLogEvery := config.GetenvInt("SNAPSHOT_LOG_EVERY", 1)
	graphHotWindow := config.GetenvDuration("GRAPH_HOT_WINDOW", 0)
	if graphHotWindow == 0 {
		graphHotWindow = config.GetenvDuration("GRAPH_RETENTION", 24*time.Hour)
	}
	if graphHotWindow <= 0 {
		slog.Error("GRAPH_HOT_WINDOW must be positive", "value", graphHotWindow)
		os.Exit(1)
	}
	mcpStdio := config.GetenvBool("MCP_STDIO", false)

	graphStore = graphstore.NewStore()
	traceStore := tracestore.NewStore()
	var snapshotSavedAt time.Time

	// Restore snapshot (non-fatal). On corrupt/missing snapshot the server
	// starts with an empty graph and re-establishes persistence once it
	// has data. persist.Save backs up the previous file as .bak before
	// overwriting, so corrupt snapshots are never lost.
	if snap, source, err := persist.LoadWithSource(snapshotPath); err == nil {
		graphStore.Restore(snap.Graph)
		snapshotSavedAt = snap.SavedAt
		slog.Info("snapshot loaded",
			"path", snapshotPath,
			"nodes", snap.NodeCount,
			"edges", snap.EdgeCount,
			"saved_at", snap.SavedAt.Format(time.RFC3339),
		)
		if source == "backup" {
			slog.Info("snapshot loaded from backup", "path", snapshotPath+".bak")
		}
	} else if errors.Is(err, persist.ErrSnapshotMissing) {
		slog.Info("no snapshot found, starting fresh")
	} else if errors.Is(err, persist.ErrSnapshotVersionMismatch) {
		slog.Warn("snapshot version incompatible, replaying from event log", "path", snapshotPath, "err", err)
	} else {
		slog.Warn("snapshot load failed, starting with empty graph", "err", err)
	}

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

	var sm *auth.SessionManager
	if authCfg.DashboardMode != "off" {
		sm = auth.NewSessionManager(authCfg.SessionSecret, auth.DefaultSessionMaxAge)
		sm.Secure = os.Getenv("WAYLOG_PROFILE") == "prod"
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
	graphUI := config.GetenvBool("GRAPH_UI", false)
	otlpEnabled := config.GetenvBool("OTLP_ENABLED", true)
	v2ReadsEnabled := config.GetenvBool("WAYLOG_V2_READS", false)

	causalEnabled := config.GetenvBool("CAUSAL_ENABLED", false)
	causalInterval := config.GetenvDuration("CAUSAL_INTERVAL", 30*time.Second)

	trustProxy := config.GetenvBool("WAYLOG_TRUST_PROXY", false)

	dedupCache := ingest.NewDedupCache()
	planStore := ingest.NewPlanStore()

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		slog.Error("mcp tools init failed", "err", err)
		os.Exit(1)
	}

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
	eventLogV2Dir := config.Getenv("EVENT_LOG_V2_DIR", defaultEventLogV2Dir(eventLogDir))
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

		slog.Info("coldstore enabled", "path", sqlitePath)
	}

	// Create ingest server with the store
	ingestServer := ingest.NewServer(ingest.ServerConfig{
		Store:               graphStore,
		TraceStore:          traceStore,
		MaxBodyBytes:        maxBody,
		EventLogDir:         eventLogDir,
		Metrics:             m,
		StartTime:           time.Now(),
		AskRegistry:         reg,
		AskMaxStepsDefault:  askMaxStepsDefault,
		AskMaxStepsMax:      askMaxStepsMax,
		DashboardRefreshSec: dashboardRefreshSec,
		PrometheusURL:       prometheusURL,
		GrafanaURL:          grafanaURL,
		GraphUI:             graphUI,
		DedupCache:          dedupCache,
		AgentKey:            agentKey,
		TrustProxy:          trustProxy,
		ColdWriter:          coldWriter,
		ColdStore:           coldDB,
		PlanStore:           planStore,
		GraphHotWindow:      graphHotWindow,
		OTLPEnabled:         otlpEnabled,
		V2ReadsEnabled:      v2ReadsEnabled,
	})

	// SSE hub for real-time dashboard updates
	sseHub := ingest.NewSSEHub(config.GetenvInt("SSE_MAX_CLIENTS", 100))
	ingestServer.SetSSEHub(sseHub)

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

		// Replay WAL to rebuild derived views.
		//
		// The graph snapshot covers nodes/edges, so the graph only needs
		// entries newer than snapshotSavedAt. The trace store is NOT
		// snapshotted, so it must be rebuilt from the full hot window
		// to restore drill-down data (trace_summary, story, topology).
		m.ReplayInProgress.Set(1)
		traceReplayAfter := time.Now().Add(-graphHotWindow)
		replayAfter := snapshotSavedAt
		if traceReplayAfter.Before(replayAfter) {
			replayAfter = traceReplayAfter
		}
		entries, replayErr := eventlog.ReadDir(eventLogDir, replayAfter)
		if replayErr != nil {
			slog.Warn("event log replay failed", "err", replayErr)
			m.ReplayFailuresTotal.Inc()
		} else if len(entries) > 0 {
			replayedGraph, replayedTrace := 0, 0
			for i := range entries {
				m.ReplayLagSeconds.Set(time.Since(entries[i].LoggedAt).Seconds())
				if !entries[i].SampledInGraph {
					continue
				}
				result := ingestServer.Builder().BuildResult(entries[i].Event)

				// Graph: only merge entries newer than the snapshot.
				if entries[i].LoggedAt.After(snapshotSavedAt) {
					graphStore.Merge(result.Graph)
					replayedGraph++
				}

				// Trace store: merge everything in the hot window.
				if result.Span != nil {
					traceStore.Upsert(entries[i].Event.Request.TraceID, core.ID("request", entries[i].Event.Request.TraceID), result.Span)
					replayedTrace++
				}
			}
			m.TraceStoreRecords.Set(float64(traceStore.Count()))
			m.TraceStoreSpans.Set(float64(traceStore.SpanCount()))
			m.TraceStoreCohorts.Set(float64(traceStore.CohortCount()))
			slog.Info("event log replay complete",
				"total", len(entries),
				"graph_replayed", replayedGraph,
				"trace_replayed", replayedTrace,
			)
		}
		m.ReplayLagSeconds.Set(0)
		m.ReplayInProgress.Set(0)
		ingestServer.SetReplayResult(replayErr)
		ingestServer.SetReady()
	} else {
		ingestServer.SetReady()
	}

	// Set default store for CLI
	cli.SetDefaultStore(graphStore)

	// ---------------- HTTP server ----------------

	// Start dedup cache eviction
	dedupCtx, dedupCancel := context.WithCancel(context.Background())
	defer dedupCancel()
	dedupCache.StartEviction(dedupCtx)

	corsOrigin := config.Getenv("CORS_ORIGIN", "*")

	writeAuth := auth.Middleware("write", authCfg.WriteKeys, nil)
	readAuth := auth.Middleware("read", authCfg.ReadKeys, sessionCheck)
	agentAuth := auth.Middleware("agent", authCfg.AgentKeys, nil)
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

	// OTLP/HTTP traces — routed through a dedicated pipeline that reuses the
	// same store, builder, sampler, WAL, cold store, and SSE hub as the SDK
	// path so counters and /v1/overview reflect OTLP traffic too.
	if otlpEnabled {
		otlpPipeline := ingest.NewPipeline(ingest.PipelineConfig{
			Store:      graphStore,
			TraceStore: traceStore,
			Builder:    ingestServer.Builder(),
			Sampler:    ingestServer.Sampler(),
			EventLog:   ingestServer.EventLog,
			ColdWriter: coldWriter,
			ColdStore:  coldDB,
			Counters:   ingestServer.Counters(),
			Accepted:   ingestServer.AcceptedPtr(),
			Metrics:    m,
			Notifier:   ingestServer.SSEHub(),
			Validator:  ingest.OTLPValidator,
		})
		otlpHandler := otelhttp.NewHandler(otlpPipeline, m, maxBody)
		mux.Handle("/v1/otlp/v1/traces", writeAuth(http.HandlerFunc(otlpHandler.ServeHTTP)))
		slog.Info("otlp enabled", "endpoint", "/v1/otlp/v1/traces")
	}

	// Read endpoints — CORS outermost so OPTIONS preflight passes without auth.
	readCORS := func(h http.HandlerFunc) http.Handler {
		inner := readAuth(http.HandlerFunc(h))
		return http.HandlerFunc(ingest.CORSWrap(corsOrigin, "GET, OPTIONS",
			func(w http.ResponseWriter, r *http.Request) { inner.ServeHTTP(w, r) }))
	}
	mux.Handle("/v1/overview", readCORS(ingestServer.Overview))
	if v2ReadsEnabled {
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
		slog.Info("v2 read endpoints enabled")
	} else {
		mux.Handle("/v1/traces/story", readCORS(ingestServer.TraceStory))
		mux.Handle("/v1/blast_radius", readCORS(ingestServer.BlastRadius))
		mux.Handle("/v1/traces/recent", readCORS(ingestServer.RecentTraces))
		mux.Handle("/v1/events/search", readCORS(ingestServer.EventSearch))
	}
	mux.Handle("/v1/overview/timeseries", readCORS(ingestServer.OverviewTimeseries))
	mux.Handle("/v1/routes", readCORS(ingestServer.Routes))
	mux.Handle("/v1/capabilities", readCORS(ingestServer.Capabilities))
	mux.Handle("/v1/topology", readCORS(ingestServer.Topology))
	mux.Handle("/v1/stream/dashboard", readCORS(ingestServer.SSEStream))
	mux.Handle("/v1/insight", readCORS(ingestServer.Insight))

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

	// Graph topology (feature-gated).
	if graphUI {
		mux.Handle("/v1/graph/topology", readCORS(ingestServer.GraphTopology))
	}

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

	go func() {
		slog.Info("ingest listening", "addr", addr, "graph_hot_window", graphHotWindow)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("ingest server error", "err", err)
			os.Exit(1)
		}
	}()

	// ---------------- Embedded CLI ----------------

	if mcpStdio {
		go func() {
			slog.Info("MCP stdio ready", "protocol", "2024-11-05")
			if err := stdio.Serve(ctx, os.Stdin, os.Stdout, reg, graphStore, stdio.ServerInfo{
				Name:    "waylog",
				Version: "0.1.0",
			}); err != nil && err != context.Canceled {
				slog.Error("mcp stdio error", "err", err)
			}
		}()
	} else {
		go replLoop()
	}

	// ---------------- Periodic snapshotter ----------------

	ticker := time.NewTicker(time.Duration(snapshotEvery) * time.Second)
	defer ticker.Stop()
	snapshotCount := 0

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snapshotCount++

				// Enforce retention: prune nodes older than the retention window.
				cutoff := time.Now().Add(-graphHotWindow)
				graphStore.PruneOlderThan(cutoff)
				v2Pruned := v2Index.PruneOlderThan(cutoff)
				if v2Pruned.Events > 0 {
					m.V2IndexPruned.Add(float64(v2Pruned.Events))
				}
				deletedTraces, _ := traceStore.PruneOlderThan(cutoff)
				m.GraphPrunedTotal.Inc()
				if deletedTraces > 0 {
					m.TraceStorePruned.Add(float64(deletedTraces))
				}

				g := graphStore.Snapshot()

				m.GraphNodes.Set(float64(len(g.Nodes)))
				m.GraphEdges.Set(float64(len(g.Edges)))
				m.TraceStoreRecords.Set(float64(traceStore.Count()))
				m.TraceStoreSpans.Set(float64(traceStore.SpanCount()))
				m.TraceStoreCohorts.Set(float64(traceStore.CohortCount()))

				if len(g.Nodes) == 0 {
					if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
						slog.Debug("snapshot skipped, graph empty")
					}
					continue
				}

				if err := persist.Save(snapshotPath, g); err != nil {
					slog.Error("snapshot save failed", "err", err, "path", snapshotPath)
					m.SnapshotLastError.Set(float64(time.Now().Unix()))
				} else {
					m.SnapshotLastSuccess.Set(float64(time.Now().Unix()))
					if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
						slog.Info("snapshot saved",
							"nodes", len(g.Nodes),
							"edges", len(g.Edges),
							"path", snapshotPath,
						)
					}
				}
			}
		}
	}()

	// ---------------- SSE recompute ticker ----------------

	go func() {
		sseTicker := time.NewTicker(1 * time.Second)
		defer sseTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sseTicker.C:
				dirty := sseHub.DrainDirty()
				if len(dirty) == 0 {
					continue
				}
				for _, topic := range dirty {
					data := ingestServer.ComputeSSETopic(topic)
					if data != nil {
						sseHub.Publish(topic, data)
					}
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

	detectCfg := detect.ParseConfig()
	if detectCfg.Enabled {
		var deploySrc detect.DeploySource
		if coldDB != nil {
			deploySrc = coldDB
		}
		detector := detect.NewDetector(detectCfg, graphStore, traceStore, deploySrc)
		ingestServer.SetDetector(detector)
		go detector.Run(ctx)
	}

	// ---------------- Causal inference ticker ----------------

	if causalEnabled && coldDB != nil {
		ingestServer.SetCausalEnabled()
		go func() {
			causalTicker := time.NewTicker(causalInterval)
			defer causalTicker.Stop()
			slog.Info("causal engine started", "interval", causalInterval)
			for {
				select {
				case <-ctx.Done():
					return
				case <-causalTicker.C:
					func() {
						defer func() {
							if r := recover(); r != nil {
								slog.Error("causal inference panicked", "recover", r)
								m.CausalRunFailures.Inc()
								ingestServer.SetCausalRunResult(fmt.Errorf("panic: %v", r))
							}
						}()

						tickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
						defer cancel()

						now := time.Now().UTC()
						m.CausalRunsTotal.Inc()

						// Query deployments first — cheap; skip snapshot if none.
						window := 1 * time.Hour
						deps, err := coldDB.DeploymentsInWindow(tickCtx, now.Add(-window), now, "")
						if err != nil {
							slog.Warn("causal: deployment query failed", "err", err)
							m.CausalRunFailures.Inc()
							ingestServer.SetCausalRunResult(err)
							return
						}
						if len(deps) == 0 {
							ingestServer.SetCausalRunResult(nil)
							return
						}

						snap := graphStore.Snapshot()
						if len(snap.Nodes) == 0 {
							ingestServer.SetCausalRunResult(nil)
							return
						}

						// Convert coldstore.Deployment → causal.DeploymentInfo
						infos := make([]causal.DeploymentInfo, len(deps))
						for i, d := range deps {
							infos[i] = causal.DeploymentInfo{ID: d.ID, Service: d.Service, FirstSeen: d.FirstSeen}
						}

						claims := causal.InferIntroducedBy(snap, infos, now.Add(-window), now)

						if len(claims) > 0 {
							if err := coldDB.SaveClaims(tickCtx, claims); err != nil {
								slog.Warn("causal: save claims failed", "err", err)
								m.CausalRunFailures.Inc()
								ingestServer.SetCausalRunResult(err)
								return
							}
							for _, c := range claims {
								slog.Info("causal claim (shadow)",
									"type", c.ClaimType,
									"subject", c.Subject,
									"target", c.Target,
									"service", c.Service,
									"confidence", c.Confidence,
									"tier", c.Tier,
								)
								m.CausalClaimsTotal.With(prometheus.Labels{
									"type": string(c.ClaimType),
									"tier": string(c.Tier),
								}).Inc()
							}
						}

						m.CausalRunDuration.Observe(time.Since(now).Seconds())
						ingestServer.SetCausalRunResult(nil)
					}()
				}
			}
		}()
	} else if causalEnabled && coldDB == nil {
		slog.Warn("CAUSAL_ENABLED=true but SQLITE_PATH not set — causal engine disabled")
	}

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

	planStore.Close()

	if coldWriter != nil {
		coldWriter.Stop()
		slog.Info("coldstore writer drained")
	}

	// Final snapshot on shutdown
	g := graphStore.Snapshot()
	if len(g.Nodes) == 0 {
		slog.Info("final snapshot skipped, graph empty")
	} else if err := persist.Save(snapshotPath, g); err != nil {
		slog.Error("final snapshot save failed", "err", err)
	} else {
		slog.Info("final snapshot saved",
			"nodes", len(g.Nodes),
			"edges", len(g.Edges),
		)
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

func defaultEventLogV2Dir(eventLogDir string) string {
	if eventLogDir != "" {
		return filepath.Join(eventLogDir, "v2")
	}
	return "./data/eventlog-v2"
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
