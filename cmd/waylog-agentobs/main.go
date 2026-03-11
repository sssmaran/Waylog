package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	agentdash "github.com/sssmaran/WaylogCLI/internal/agentobs/dashboard"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/handler"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/metrics"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/persist"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
	"github.com/sssmaran/WaylogCLI/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	addr := config.Getenv("AGENT_OBS_ADDR", ":9090")
	apiKey := config.Getenv("AGENT_OBS_API_KEY", "")
	walDir := config.Getenv("AGENT_OBS_EVENT_LOG_DIR", "./data/agent_events")
	walSync := config.GetenvBool("AGENT_OBS_EVENT_LOG_SYNC", true)
	walMaxMB := config.GetenvInt("AGENT_OBS_EVENT_LOG_MAX_FILE_MB", 50)
	snapshotPath := config.Getenv("AGENT_OBS_SNAPSHOT_PATH", "./data/agent_snapshot.json")
	retention := config.GetenvDuration("AGENT_OBS_RETENTION", 24*time.Hour)
	redact := config.GetenvBool("AGENT_OBS_REDACT_PAYLOADS", false)
	abandonThreshold := config.GetenvDuration("AGENT_OBS_ABANDON_THRESHOLD", 120*time.Second)
	corsOrigin := config.Getenv("AGENT_OBS_CORS_ORIGIN", "*")

	// 1. Restore store from snapshot
	s, err := persist.Load(snapshotPath)
	if err != nil {
		if errors.Is(err, persist.ErrSnapshotMissing) {
			slog.Info("no snapshot found, starting fresh")
			s = store.New()
		} else {
			slog.Error("snapshot corrupted, refusing to start with empty state", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Info("snapshot restored", "runs", s.RunCount(), "sessions", s.SessionCount())
	}

	// 2. WAL writer
	walCfg := eventlog.WriterConfig{
		SyncOnWrite:  walSync,
		MaxFileBytes: int64(walMaxMB) * 1024 * 1024,
	}
	wal, err := eventlog.NewWithConfig(walDir, walCfg)
	if err != nil {
		slog.Error("failed to create WAL", "err", err)
		os.Exit(1)
	}

	// 3. Replay WAL
	entries, replayStats, err := eventlog.ReadDir(walDir, time.Time{})
	if err != nil {
		slog.Warn("wal replay failed", "err", err)
	} else {
		for _, entry := range entries {
			s.Merge(&entry.Event)
		}
		slog.Info("wal replay complete",
			"events", replayStats.EntriesLoaded,
			"files_read", replayStats.FilesRead,
			"files_errored", replayStats.FilesErrored,
			"lines_corrupted", replayStats.LinesCorrupted,
		)
	}
	dedupIndex := eventlog.BuildDedupIndex(entries)

	// 4. Metrics
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	// 5. Handler
	h := handler.NewHandler(s, wal, m, handler.HandlerConfig{
		APIKey:         apiKey,
		RedactPayloads: redact,
		CostRates:      parseCostRates(os.Getenv("AGENT_OBS_COST_RATES")),
	})
	h.SetDedupIndex(dedupIndex)

	// 6. Routes
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/events", h.Ingest)
	mux.HandleFunc("GET /v1/agent/runs", h.ListRuns)
	mux.HandleFunc("GET /v1/agent/runs/{id}", h.GetRun)
	mux.HandleFunc("GET /v1/agent/sessions/{id}", h.GetSession)
	mux.HandleFunc("GET /v1/agent/sessions/{id}/waterfall", h.GetWaterfall)
	mux.HandleFunc("GET /v1/agent/stats", h.GetStats)
	mux.HandleFunc("GET /v1/agent/cost", h.GetCost)
	mux.HandleFunc("GET /v1/agent/tools", h.GetToolAnalytics)
	mux.HandleFunc("GET /livez", h.Livez)
	mux.HandleFunc("GET /readyz", h.Readyz)
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.Handle("GET /metrics", m.Handler())
	mux.Handle("/ui/", http.StripPrefix("/ui/", agentdash.Handler()))

	// 7. Server — CORS middleware wraps the mux so OPTIONS preflight reaches all paths
	srv := &http.Server{
		Addr:              addr,
		Handler:           corsMiddleware(corsOrigin, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background: periodic snapshot + prune
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.PruneOlderThan(retention)
				if err := persist.Save(snapshotPath, s); err != nil {
					slog.Error("snapshot_save_failed", "err", err)
				}
			}
		}
	}()

	// Background: abandoned session scanner
	go func() {
		ticker := time.NewTicker(abandonThreshold / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.ScanAbandoned(abandonThreshold)
			}
		}
	}()

	slog.Info("waylog-agentobs starting", "addr", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server_error", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	persist.Save(snapshotPath, s)
	wal.Close()
	slog.Info("shutdown complete")
}

// corsMiddleware adds CORS headers to /v1/agent/ paths and handles OPTIONS preflight.
func corsMiddleware(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/agent/") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// parseCostRates parses the AGENT_OBS_COST_RATES env var.
// Format: "model=inputPer1K,outputPer1K;model=inputPer1K,outputPer1K"
// Example: "claude-sonnet-4-6=0.003,0.015;gpt-4o=0.005,0.015"
func parseCostRates(raw string) map[string]handler.CostRate {
	if raw == "" {
		return nil
	}
	rates := make(map[string]handler.CostRate)
	for _, pair := range strings.Split(raw, ";") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		costs := strings.Split(parts[1], ",")
		if len(costs) != 2 {
			continue
		}
		inCost, err1 := strconv.ParseFloat(costs[0], 64)
		outCost, err2 := strconv.ParseFloat(costs[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rates[parts[0]] = handler.CostRate{InputPer1K: inCost, OutputPer1K: outCost}
	}
	return rates
}
