package persist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
)

const SnapshotVersion = "1"

var ErrSnapshotMissing = errors.New("snapshot missing")

type Snapshot struct {
	Version  string              `json:"version"`
	SavedAt  time.Time           `json:"saved_at"`
	Checksum string              `json:"checksum"`
	Data     *store.SnapshotData `json:"data"`
}

func Save(path string, s *store.Store) error {
	if path == "" {
		return errors.New("snapshot path is empty")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}

	data := s.Snapshot()

	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(raw)

	snap := Snapshot{
		Version:  SnapshotVersion,
		SavedAt:  time.Now().UTC(),
		Checksum: hex.EncodeToString(sum[:]),
		Data:     data,
	}

	out, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp snapshot: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync temp snapshot: %w", err)
	}
	f.Close()

	// atomic backup: rename is atomic on POSIX
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".bak"); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("backup snapshot: %w", err)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp snapshot: %w", err)
	}

	return nil
}

func Load(path string) (*store.Store, error) {
	if s, err := decodeSnapshot(path); err == nil {
		return s, nil
	} else if s2, err2 := decodeSnapshot(path + ".bak"); err2 == nil {
		return s2, nil
	} else {
		if isMissing(err) && isMissing(err2) {
			return nil, ErrSnapshotMissing
		}
		return nil, fmt.Errorf("load snapshot failed: %v; backup failed: %v", err, err2)
	}
}

func decodeSnapshot(path string) (*store.Store, error) {
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

	raw, err := json.Marshal(snap.Data)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != snap.Checksum {
		return nil, errors.New("snapshot checksum mismatch")
	}

	s := store.New()
	if snap.Data != nil {
		s.Restore(snap.Data)
	}
	return s, nil
}

func isMissing(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}
