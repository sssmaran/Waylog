package main

import (
	"errors"
	"log/slog"
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
		slog.Info("snapshot loaded",
			"path", snapshotPath,
			"nodes", snap.NodeCount,
			"edges", snap.EdgeCount,
			"saved_at", snap.SavedAt.Format(time.RFC3339),
		)
		if source == "backup" {
			slog.Info("snapshot loaded from backup", "path", snapshotPath+".bak")
		}
	} else if errors.Is(err, persist.ErrSnapshotMissing) {
		slog.Info("no snapshot found, starting fresh")
	} else {
		slog.Warn("no snapshot loaded", "err", err)
	}

	cli.SetDefaultStore(store)
	cli.Run(os.Args[1:])
}
