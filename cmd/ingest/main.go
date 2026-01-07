package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/ingest"
)

func main() {
	addr := getenv("INGEST_ADDR", ":8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ingest.Health)
	mux.HandleFunc("/v1/events", ingest.Events)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		log.Printf("ingest listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ingest server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("ingest shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
//with this graceful shutdown - No request finishes without its event being ingested.
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("ingest graceful shutdown failed: %v", err)
	} else {
		log.Println("ingest shutdown complete")
	}
}


func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
