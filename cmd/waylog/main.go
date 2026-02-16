package main

import (
	"errors"
	"log"
	"os"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/cli"
	"github.com/sssmaran/WaylogCLI/internal/config"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/persist"
)

func main() {
	config.LoadDotEnv(".env")

	snapshotPath := config.Getenv("SNAPSHOT_PATH", "./data/graph_snapshot.json")
	store := graphstore.NewStore()

	if snap, source, err := persist.LoadWithSource(snapshotPath); err == nil {
		store.Restore(snap.Graph)
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
		log.Printf("SNAPSHOT: none found, starting fresh")
	} else {
		log.Printf("SNAPSHOT: no snapshot loaded (%v)", err)
	}

	cli.SetDefaultStore(store)
	cli.Run(os.Args[1:])
}
