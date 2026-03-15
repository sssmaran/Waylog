package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/dashboard"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/persist"
	"github.com/sssmaran/WaylogCLI/internal/tools"
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
	graphRetention := config.GetenvDuration("GRAPH_RETENTION", 24*time.Hour)
	if graphRetention <= 0 {
		slog.Error("GRAPH_RETENTION must be positive", "value", graphRetention)
		os.Exit(1)
	}
	mcpStdio := config.GetenvBool("MCP_STDIO", false)

	graphStore = graphstore.NewStore()
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
	} else {
		slog.Warn("snapshot load failed, starting with empty graph", "err", err)
	}

	apiKey := config.Getenv("WAYLOG_API_KEY", "")
	agentKey := config.Getenv("WAYLOG_AGENT_KEY", "")
	maxBody := int64(config.GetenvInt("MAX_BODY_BYTES", 1<<20))
	eventLogDir := config.Getenv("EVENT_LOG_DIR", "")
	sqlitePath := config.Getenv("SQLITE_PATH", "")
	askMaxStepsDefault := config.GetenvInt("ASK_MAX_STEPS_DEFAULT", 5)
	askMaxStepsMax := config.GetenvInt("ASK_MAX_STEPS_MAX", 8)
	dashboardRefreshSec := config.GetenvInt("DASHBOARD_REFRESH_SEC", 10)
	prometheusURL := config.Getenv("PROMETHEUS_URL", "")
	grafanaURL := config.Getenv("GRAFANA_URL", "")
	graphUI := config.GetenvBool("GRAPH_UI", false)

	trustProxy := config.GetenvBool("WAYLOG_TRUST_PROXY", false)

	dedupCache := ingest.NewDedupCache()

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		slog.Error("mcp tools init failed", "err", err)
		os.Exit(1)
	}

	// Prometheus metrics
	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg)

	// Optional SQLite cold store
	var coldDB *coldstore.Store
	var coldWriter *coldstore.BatchWriter
	if sqlitePath != "" {
		if eventLogDir == "" {
			slog.Warn("SQLITE_PATH set without EVENT_LOG_DIR — cold store is async-only, "+
				"events may be lost on crash. Set EVENT_LOG_DIR for durable writes")
		}
		var err error
		coldDB, err = coldstore.Open(sqlitePath)
		if err != nil {
			slog.Error("coldstore init failed", "err", err)
			os.Exit(1)
		}
		defer coldDB.Close()

		coldWriter = coldstore.NewBatchWriter(coldDB, coldstore.BatchWriterConfig{
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
		APIKey:              apiKey,
	})

	// Optional append-only event log
	eventLogSync := config.GetenvBool("EVENT_LOG_SYNC", true)
	eventLogMaxMB := int64(config.GetenvInt("EVENT_LOG_MAX_FILE_MB", 50))
	eventLogRetention := config.GetenvDuration("EVENT_LOG_RETENTION", 72*time.Hour)
	if eventLogRetention <= 0 {
		slog.Error("EVENT_LOG_RETENTION must be positive", "value", eventLogRetention)
		os.Exit(1)
	}
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

		// Replay events newer than last snapshot (or all if no snapshot)
		m.ReplayInProgress.Set(1)
		entries, replayErr := eventlog.ReadDir(eventLogDir, snapshotSavedAt)
		if replayErr != nil {
			slog.Warn("event log replay failed", "err", replayErr)
			m.ReplayFailuresTotal.Inc()
		} else if len(entries) > 0 {
			replayed := 0
			for i := range entries {
				m.ReplayLagSeconds.Set(time.Since(entries[i].LoggedAt).Seconds())
				if !entries[i].SampledInGraph {
					continue
				}
				g := ingestServer.Builder().Build(entries[i].Event)
				graphStore.Merge(g)
				replayed++
			}
			slog.Info("event log replay complete", "total", len(entries), "replayed", replayed)
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ingestServer.Health)
	mux.HandleFunc("/livez", ingestServer.Livez)
	mux.HandleFunc("/readyz", ingestServer.Readyz)
	mux.Handle("/metrics", m.Handler())
	if apiKey != "" {
		mux.HandleFunc("/v1/events", ingest.APIKeyMiddleware(apiKey, ingestServer.Events))
	} else {
		mux.HandleFunc("/v1/events", ingestServer.Events)
	}

	mux.HandleFunc("/v1/events/validate", ingestServer.Validate)

	// Read APIs with CORS
	corsOrigin := config.Getenv("CORS_ORIGIN", "*")
	mux.HandleFunc("/v1/traces/story", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.TraceStory))
	mux.HandleFunc("/v1/traces/recent", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.RecentTraces))
	mux.HandleFunc("/v1/overview", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.Overview))
	mux.HandleFunc("/v1/events/search", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.EventSearch))
	mux.HandleFunc("/v1/overview/timeseries", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.OverviewTimeseries))
	mux.HandleFunc("/v1/routes", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.Routes))
	mux.HandleFunc("/v1/capabilities", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.Capabilities))
	mux.HandleFunc("/v1/deployments", ingest.CORSWrap(corsOrigin, "GET, POST, OPTIONS", ingestServer.DeployRoute))

	// Agent-authenticated endpoints: CORS outermost, then auth
	if agentKey != "" {
		mux.HandleFunc("/v1/tools", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingest.APIKeyMiddleware(agentKey, ingestServer.Tools)))
		mux.HandleFunc("/v1/tools/", ingest.CORSWrap(corsOrigin, "POST, OPTIONS", ingest.APIKeyMiddleware(agentKey, ingestServer.ToolCall)))
		mux.HandleFunc("/v1/ask", ingest.CORSWrap(corsOrigin, "POST, OPTIONS", ingest.APIKeyMiddleware(agentKey, ingestServer.Ask)))
	} else {
		mux.HandleFunc("/v1/tools", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.Tools))
		mux.HandleFunc("/v1/tools/", ingest.CORSWrap(corsOrigin, "POST, OPTIONS", ingestServer.ToolCall))
		mux.HandleFunc("/v1/ask", ingest.CORSWrap(corsOrigin, "POST, OPTIONS", ingestServer.Ask))
	}

	if graphUI {
		mux.HandleFunc("/v1/graph/topology", ingest.CORSWrap(corsOrigin, "GET, OPTIONS", ingestServer.GraphTopology))
	}

	// Dashboard UI
	mux.Handle("/ui/", http.StripPrefix("/ui/", dashboard.Handler()))
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/ui/ask", ingest.CORSWrap(corsOrigin, "POST, OPTIONS", ingestServer.DashboardAsk))

	// Wrap mux with CorrelationID middleware
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
		slog.Info("ingest listening", "addr", addr, "graph_retention", graphRetention)
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
				graphStore.PruneOlderThan(time.Now().Add(-graphRetention))
				m.GraphPrunedTotal.Inc()

				g := graphStore.Snapshot()

				m.GraphNodes.Set(float64(len(g.Nodes)))
				m.GraphEdges.Set(float64(len(g.Edges)))

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

	// ---------------- Event log retention ----------------

	if el != nil {
		go func() {
			retTicker := time.NewTicker(5 * time.Minute)
			defer retTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-retTicker.C:
					n, err := eventlog.PruneOlderThan(eventLogDir, eventLogRetention, el.ActivePath())
					if err != nil {
						slog.Warn("eventlog retention cleanup error", "err", err)
					} else if n > 0 {
						slog.Info("eventlog retention cleanup", "deleted", n)
					}
				}
			}
		}()
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
