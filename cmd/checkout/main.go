package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/checkout"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
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
		cfg.Kafka = waylog.KafkaConfig{
			Brokers: brokers,
			Topic:   config.Getenv("KAFKA_TOPIC", "wide_events"),
		}
	}

	err := waylog.Init(cfg)
	if err != nil {
		log.Fatalf("waylog init failed: %v", err)
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
		log.Println("checkout service listening on :9090")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("checkout server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("checkout shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("checkout graceful shutdown failed: %v", err)
	}
	if err := waylog.Shutdown(shutdownCtx); err != nil {
		log.Printf("waylog shutdown failed: %v", err)
	}
	log.Println("checkout shutdown complete")
}

