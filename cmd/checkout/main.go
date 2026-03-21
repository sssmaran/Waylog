package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/checkout"
	"github.com/sssmaran/WaylogCLI/internal/config"
	waylog "github.com/sssmaran/WaylogCLI/pkg"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
	kafkatransport "github.com/sssmaran/WaylogCLI/pkg/transport/kafka"
)

type coded interface {
	Code() string
}

func main() {
	cfg := waylog.Config{
		Service:      "checkout-service",
		Env:          "dev",
		Version:      "0.1.0",
		DeploymentID: os.Getenv("DEPLOYMENT_ID"),
		ErrorClassifier: func(err error) string {
			if err == nil {
				return ""
			}
			if codedErr, ok := err.(coded); ok {
				return codedErr.Code()
			}
			return ""
		},
	}

	if brokers := config.SplitEnvList("KAFKA_BROKERS"); len(brokers) > 0 {
		kt, err := kafkatransport.New(kafkatransport.Config{
			Brokers: brokers,
			Topic:   config.Getenv("KAFKA_TOPIC", "wide_events"),
		})
		if err != nil {
			slog.Error("kafka transport init failed", "err", err)
			os.Exit(1)
		}
		cfg.Transport = kt
	}

	err := waylog.Init(cfg)
	if err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	svc := checkout.NewService()
	handler := checkout.NewHandler(svc)

	mux := http.NewServeMux()
	mux.Handle("/checkout", wayloghttp.Middleware(handler))
	server := &http.Server{
		Addr:    ":9090",
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		slog.Info("checkout service listening", "addr", ":9090")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("checkout server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("checkout shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("checkout graceful shutdown failed", "err", err)
	}
	if err := waylog.Shutdown(shutdownCtx); err != nil {
		slog.Error("waylog shutdown failed", "err", err)
	}
	slog.Info("checkout shutdown complete")
}
