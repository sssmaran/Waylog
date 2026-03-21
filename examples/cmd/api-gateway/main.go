package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
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
		Service:      "api-gateway",
		Env:          "dev",
		Version:      "0.1.0",
		DeploymentID: os.Getenv("DEPLOYMENT_ID"),
		ErrorClassifier: func(err error) string {
			if err == nil {
				return ""
			}
			if c, ok := err.(coded); ok {
				return c.Code()
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

	if err := waylog.Init(cfg); err != nil {
		slog.Error("waylog init failed", "err", err)
		os.Exit(1)
	}

	checkoutURL := config.Getenv("CHECKOUT_URL", "http://localhost:9082")
	gateway := microdemo.NewGatewayHandler(checkoutURL)

	mux := http.NewServeMux()
	mux.Handle("/purchase", wayloghttp.Middleware(http.HandlerFunc(gateway.ServePurchase)))
	mux.HandleFunc("/demo", gateway.ServeDemo)

	server := &http.Server{
		Addr:    ":9081",
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("api-gateway listening", "addr", ":9081")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("api-gateway server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("api-gateway shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("api-gateway graceful shutdown failed", "err", err)
	}
	if err := waylog.Shutdown(shutdownCtx); err != nil {
		slog.Error("waylog shutdown failed", "err", err)
	}
	slog.Info("api-gateway shutdown complete")
}
