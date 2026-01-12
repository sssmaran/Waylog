package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

const SnapshotVersion = "1"
var ErrSnapshotMissing = errors.New("snapshot missing")

type Snapshot struct {
	Version   string       `json:"version"`
	SavedAt   time.Time    `json:"saved_at"`
	Checksum  string       `json:"checksum"`
	Graph     *core.Graph `json:"graph"`

	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
}

func Save(path string, g *core.Graph) error {
	if path == "" {
		return errors.New("snapshot path is empty")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}

	tmp := Snapshot{
		Version:   SnapshotVersion,
		SavedAt:   time.Now().UTC(),
		Graph:     g,
		NodeCount: len(g.Nodes),
		EdgeCount: len(g.Edges),
	}

	raw, err := json.Marshal(tmp.Graph)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(raw)
	tmp.Checksum = hex.EncodeToString(sum[:])

	out, err := json.MarshalIndent(tmp, "", "  ")
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		_ = copyFile(path, path+".bak")
	}

	return os.WriteFile(path, out, 0644)
}

func Load(path string) (*Snapshot, error) {
	snap, _, err := LoadWithSource(path)
	return snap, err
}

func LoadWithSource(path string) (*Snapshot, string, error) {
	if snap, err := loadSnapshot(path); err == nil {
		return snap, "primary", nil
	} else if bakSnap, err2 := loadSnapshot(path + ".bak"); err2 == nil {
		return bakSnap, "backup", nil
	} else {
		if isMissing(err) && isMissing(err2) {
			return nil, "", ErrSnapshotMissing
		}
		return nil, "", fmt.Errorf("load snapshot failed: %v; backup failed: %v", err, err2)
	}
}

func loadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, err
	}

	if snap.Version != SnapshotVersion {
		return nil, errors.New("snapshot version mismatch")
	}

	raw, err := json.Marshal(snap.Graph)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != snap.Checksum {
		return nil, errors.New("snapshot checksum mismatch")
	}

	return &snap, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func isMissing(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}
