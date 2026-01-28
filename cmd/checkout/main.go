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
	"github.com/sssmaran/WaylogCLI/internal/trace"
	"github.com/sssmaran/WaylogCLI/pkg/sdk"
)

func main() {
	events := sdk.New("http://localhost:8080")
	svc := checkout.NewService()
	handler := checkout.NewHandler(svc, events)
	tracedHandler := trace.Middleware(handler)

	mux := http.NewServeMux()
	mux.Handle("/checkout", tracedHandler)
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
 // with this graceful shutdown - No request finishes without its event being emitted.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("checkout graceful shutdown failed: %v", err)
	} else {
		log.Println("checkout shutdown complete")
	}
}
