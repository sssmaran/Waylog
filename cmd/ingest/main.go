package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/mcp/stdio"
	"github.com/sssmaran/WaylogCLI/internal/persist"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func main() {
	log.SetOutput(os.Stderr)
	addr := getenv("INGEST_ADDR", ":8080")

	// ---------------- Graph persistence config ----------------

	snapshotPath := getenv("SNAPSHOT_PATH", "./data/graph_snapshot.json")
	snapshotEvery := getenvInt("SNAPSHOT_EVERY_SEC", 5)
	// retention := getenvDuration("GRAPH_RETENTION", 0)
	mcpStdio := getenvBool("MCP_STDIO", false)

	store := ingest.GlobalGraphStore
	snapshotLoaded := false
	// Restore snapshot (non-fatal)
	if snap, source, err := persist.LoadWithSource(snapshotPath); err == nil {
		store.Restore(snap.Graph)
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

	// Share store with ingest + CLI  //added
	ingest.SetStore(store)
	cli.SetStore(store)

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		log.Fatalf("mcp tools init failed: %v", err)
	}

	// ---------------- HTTP server ----------------

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

	// ---------------- Embedded CLI ----------------

	if mcpStdio {
		go func() {
			if err := stdio.Serve(ctx, os.Stdin, os.Stdout, reg, store, stdio.ServerInfo{
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

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// if retention > 0 {
				// 	store.PruneOlderThan(time.Now().Add(-retention))
				// }
				g := store.Snapshot()
				if !snapshotLoaded {
					log.Println("SNAPSHOT: skipped (last load failed)")
					continue
				}
				if len(g.Nodes) == 0 {
					log.Println("SNAPSHOT: skipped (graph empty)")
					continue
				}

				if err := persist.Save(snapshotPath, g); err != nil {
					log.Printf("SNAPSHOT: save failed: %v", err)
				} else {
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

	// Final snapshot on shutdown  //added
	g := store.Snapshot()
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
	os.Stdout.WriteString("  graph stats\n")
	os.Stdout.WriteString("  graph failures [--tier=premium]\n")
	os.Stdout.WriteString("  graph explain <request-id>\n")
	os.Stdout.WriteString("  graph blast <error-code> [--services] [--top-users=N] [--by-tier]\n")
	os.Stdout.WriteString("  graph chain <request-id>\n")
	os.Stdout.WriteString("  ask \"<question>\"\n")
	os.Stdout.WriteString("  help\n")
	os.Stdout.WriteString("  exit\n")

}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(k string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

// func getenvDuration(k string, def time.Duration) time.Duration {
// 	if v := os.Getenv(k); v != "" {
// 		if d, err := time.ParseDuration(v); err == nil {
// 			return d
// 		}
// 	}
// 	return def
// }
