package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

func TestLoadWithSourceRejectsVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph_snapshot.json")
	writeSnapshotFile(t, path, "1")

	snap, source, err := LoadWithSource(path)
	if err == nil {
		t.Fatalf("LoadWithSource returned snapshot=%v source=%q, want version mismatch error", snap, source)
	}
	if !errors.Is(err, ErrSnapshotVersionMismatch) {
		t.Fatalf("errors.Is(err, ErrSnapshotVersionMismatch) = false, err=%v", err)
	}
	if errors.Is(err, ErrSnapshotMissing) {
		t.Fatalf("version mismatch should not be reported as missing, err=%v", err)
	}
}

func TestLoadWithSourceMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph_snapshot.json")

	snap, source, err := LoadWithSource(path)
	if !errors.Is(err, ErrSnapshotMissing) {
		t.Fatalf("errors.Is(err, ErrSnapshotMissing) = false, err=%v", err)
	}
	if snap != nil || source != "" {
		t.Fatalf("LoadWithSource returned snapshot=%v source=%q, want nil/empty on missing", snap, source)
	}
}

func writeSnapshotFile(t *testing.T, path, version string) {
	t.Helper()

	g := core.New()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}

	sum := sha256.Sum256(raw)
	snap := Snapshot{
		Version:   version,
		SavedAt:   time.Unix(1700000000, 0).UTC(),
		Checksum:  hex.EncodeToString(sum[:]),
		Graph:     g,
		NodeCount: len(g.Nodes),
		EdgeCount: len(g.Edges),
	}

	out, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}
