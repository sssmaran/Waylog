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

	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
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
	mcpStdio := config.GetenvBool("MCP_STDIO", false)

	graphStore = graphstore.NewStore()
	snapshotLoaded := false

	// Restore snapshot (non-fatal)
	if snap, source, err := persist.LoadWithSource(snapshotPath); err == nil {
		graphStore.Restore(snap.Graph)
		snapshotLoaded = true
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
		snapshotLoaded = true
		slog.Info("no snapshot found, starting fresh")
	} else {
		slog.Warn("no snapshot loaded", "err", err)
	}

	apiKey := config.Getenv("WAYLOG_API_KEY", "")
	maxBody := int64(config.GetenvInt("MAX_BODY_BYTES", 1<<20))

	// Create ingest server with the store
	ingestServer := ingest.NewServer(ingest.ServerConfig{
		Store:        graphStore,
		MaxBodyBytes: maxBody,
	})

	// Optional append-only event log
	if dir := config.Getenv("EVENT_LOG_DIR", ""); dir != "" {
		el, err := eventlog.New(dir)
		if err != nil {
			slog.Error("eventlog init failed", "err", err)
			os.Exit(1)
		}
		defer el.Close()
		ingestServer.EventLog = el
		slog.Info("eventlog enabled", "dir", dir)
	}

	// Set default store for CLI
	cli.SetDefaultStore(graphStore)

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		slog.Error("mcp tools init failed", "err", err)
		os.Exit(1)
	}

	// ---------------- HTTP server ----------------

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ingestServer.Health)
	if apiKey != "" {
		mux.HandleFunc("/v1/events", ingest.APIKeyMiddleware(apiKey, ingestServer.Events))
	} else {
		mux.HandleFunc("/v1/events", ingestServer.Events)
	}

	mux.HandleFunc("/v1/events/validate", ingestServer.Validate)

	// Read APIs with CORS
	corsOrigin := config.Getenv("CORS_ORIGIN", "*")
	mux.HandleFunc("/v1/traces/story", ingest.CORSWrap(corsOrigin, ingestServer.TraceStory))
	mux.HandleFunc("/v1/traces/recent", ingest.CORSWrap(corsOrigin, ingestServer.RecentTraces))
	mux.HandleFunc("/v1/overview", ingest.CORSWrap(corsOrigin, ingestServer.Overview))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
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
		slog.Info("ingest listening", "addr", addr)
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
				g := graphStore.Snapshot()
				if !snapshotLoaded {
					if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
						slog.Warn("snapshot skipped, last load failed")
					}
					continue
				}
				if len(g.Nodes) == 0 {
					if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
						slog.Debug("snapshot skipped, graph empty")
					}
					continue
				}

				if err := persist.Save(snapshotPath, g); err != nil {
					slog.Error("snapshot save failed", "err", err, "path", snapshotPath)
				} else if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
					slog.Info("snapshot saved",
						"nodes", len(g.Nodes),
						"edges", len(g.Edges),
						"path", snapshotPath,
					)
				}
			}
		}
	}()

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

	// Final snapshot on shutdown
	g := graphStore.Snapshot()
	if !snapshotLoaded {
		slog.Warn("final snapshot skipped, last load failed")
	} else if len(g.Nodes) == 0 {
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
