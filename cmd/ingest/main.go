package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/config"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
	"github.com/sssmaran/WaylogCLI/internal/persist"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

var graphStore *graphstore.Store

func main() {
	log.SetOutput(os.Stderr)
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
		log.Printf(
			"SNAPSHOT: loaded %s (nodes=%d edges=%d saved_at=%s)",
			snapshotPath,
			snap.NodeCount,
			snap.EdgeCount,
			snap.SavedAt.Format(time.RFC3339),
		)
		if source == "backup" {
			log.Printf("SNAPSHOT: loaded from backup %s.bak", snapshotPath)
		}
	} else if errors.Is(err, persist.ErrSnapshotMissing) {
		snapshotLoaded = true
		log.Printf("SNAPSHOT: none found, starting fresh")
	} else {
		log.Printf("SNAPSHOT: no snapshot loaded (%v)", err)
	}

	// Create ingest server with the store
	ingestServer := ingest.NewServer(ingest.ServerConfig{
		Store: graphStore,
	})

	// Set default store for CLI
	cli.SetDefaultStore(graphStore)

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		log.Fatalf("mcp tools init failed: %v", err)
	}

	// ---------------- HTTP server ----------------

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", ingestServer.Health)
	mux.HandleFunc("/v1/events", ingestServer.Events)

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

	// ---------------- Embedded CLI ----------------

	if mcpStdio {
		go func() {
			log.Printf("MCP stdio ready (protocol %s)", "2024-11-05")
			if err := stdio.Serve(ctx, os.Stdin, os.Stdout, reg, graphStore, stdio.ServerInfo{
				Name:    "waylog",
				Version: "0.1.0",
			}); err != nil && err != context.Canceled {
				log.Printf("mcp stdio error: %v", err)
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
						log.Println("SNAPSHOT: skipped (last load failed)")
					}
					continue
				}
				if len(g.Nodes) == 0 {
					if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
						log.Println("SNAPSHOT: skipped (graph empty)")
					}
					continue
				}

				if err := persist.Save(snapshotPath, g); err != nil {
					log.Printf("SNAPSHOT: save failed: %v", err)
				} else if snapshotLogEvery > 0 && snapshotCount%snapshotLogEvery == 0 {
					log.Printf(
						"SNAPSHOT: saved (nodes=%d edges=%d) -> %s",
						len(g.Nodes),
						len(g.Edges),
						snapshotPath,
					)
				}
			}
		}
	}()

	// ---------------- Shutdown ----------------

	<-ctx.Done()
	log.Println("ingest shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("ingest graceful shutdown failed: %v", err)
	} else {
		log.Println("ingest shutdown complete")
	}

	// Final snapshot on shutdown
	g := graphStore.Snapshot()
	if !snapshotLoaded {
		log.Println("SNAPSHOT: final save skipped (last load failed)")
	} else if len(g.Nodes) == 0 {
		log.Println("SNAPSHOT: final save skipped (graph empty)")
	} else if err := persist.Save(snapshotPath, g); err != nil {
		log.Printf("SNAPSHOT: final save failed: %v", err)
	} else {
		log.Printf(
			"SNAPSHOT: final save ok (nodes=%d edges=%d)",
			len(g.Nodes),
			len(g.Edges),
		)
	}
}

func replLoop() {
	config.LoadDotEnv(".env")

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
	os.Stdout.WriteString("  waylog \"<question>\"\n")
	os.Stdout.WriteString("  help\n")
	os.Stdout.WriteString("  exit\n")
	os.Stdout.WriteString("\nexamples:\n")
	os.Stdout.WriteString("  waylog \"show top errors\"\n")
	os.Stdout.WriteString("  waylog \"summarize trace <trace-id>\"\n")
	os.Stdout.WriteString("  waylog \"explain request <request-id>\"\n")
	os.Stdout.WriteString("\nnotes:\n")
	os.Stdout.WriteString("  MCP stdio: run with MCP_STDIO=1 and use tools/list or tools/call\n")
}
