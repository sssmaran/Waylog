package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
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
		cfg.Kafka = waylog.KafkaConfig{
			Brokers: brokers,
			Topic:   config.Getenv("KAFKA_TOPIC", "wide_events"),
		}
	}

	if err := waylog.Init(cfg); err != nil {
		log.Fatalf("waylog init failed: %v", err)
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
		log.Println("api-gateway listening on :9081")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("api-gateway server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("api-gateway shutdown signal received")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("api-gateway graceful shutdown failed: %v", err)
	}
	if err := waylog.Shutdown(shutdownCtx); err != nil {
		log.Printf("waylog shutdown failed: %v", err)
	}
	log.Println("api-gateway shutdown complete")
}
