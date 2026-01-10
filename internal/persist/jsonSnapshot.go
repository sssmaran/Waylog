package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph"
)

const SnapshotVersion = "1.0"

type Snapshot struct {
	Version   string     `json:"version"`
	SavedAt   time.Time  `json:"saved_at"`
	Graph     *graph.Graph `json:"graph"`
	NodeCount int        `json:"node_count"`
	EdgeCount int        `json:"edge_count"`
}

func Save(path string, g *graph.Graph) error {
	if path == "" {
		return fmt.Errorf("snapshot path is empty")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}

	snap := Snapshot{
		Version:   SnapshotVersion,
		SavedAt:   time.Now().UTC(),
		Graph:     g,
		NodeCount: len(g.Nodes),
		EdgeCount: len(g.Edges),
	}


	tmp := path + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp snapshot: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")

	if err := enc.Encode(&snap); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close snapshot: %w", err)
	}

	// atomic-ish replace
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename snapshot: %w", err)
	}

	return nil
}

func Load(path string) (*Snapshot, error) {
	if path == "" {
		return nil, fmt.Errorf("snapshot path is empty")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}

	if snap.Version != SnapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version: %s", snap.Version)
	}

	return &snap, nil
}
