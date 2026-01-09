package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/cli"
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

	// Start HTTP server
	go func() {
		log.Printf("ingest listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ingest server error: %v", err)
		}
	}()

	// Start embedded CLI (same process, same memory)
	go replLoop()

	<-ctx.Done()
	log.Println("ingest shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// With graceful shutdown:
	// No request finishes without its event being ingested.
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("ingest graceful shutdown failed: %v", err)
	} else {
		log.Println("ingest shutdown complete")
	}
}

func replLoop() {
	in := bufio.NewScanner(os.Stdin)

	printHelp()
	for {
		os.Stdout.WriteString("ingest> ")

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
	os.Stdout.WriteString("commands:\n")
	os.Stdout.WriteString("  graph failures [--tier=premium]\n")
	os.Stdout.WriteString("  graph explain <request-id>\n")
	os.Stdout.WriteString("  help\n")
	os.Stdout.WriteString("  exit\n")
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
