package microdemo

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/config"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

func InitService(service string) error {
	return waylogv2.Init(waylogv2.Config{
		Service:   service,
		Env:       "demo",
		Version:   "0.1.0",
		IngestURL: config.Getenv("INGEST_URL", "http://localhost:8080"),
		APIKey:    config.Getenv("WAYLOG_WRITE_KEY", ""),
		DevMode:   config.GetenvBool("WAYLOG_DEV", false),
	})
}

func RunService(name, addr string, handler http.Handler) {
	server := &http.Server{Addr: addr, Handler: handler}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info(name+" listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error(name+" server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error(name+" graceful shutdown failed", "err", err)
	}
	if err := waylogv2.Shutdown(shutdownCtx); err != nil {
		slog.Error("waylog shutdown failed", "err", err)
	}
}
