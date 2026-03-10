package persist

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
	"github.com/sssmaran/WaylogCLI/internal/agentobs/store"
)

func TestSaveAndLoad(t *testing.T) {
	s := store.New()
	now := time.Now()

	// merge run.start
	if err := s.Merge(&agentobs.AgentEvent{
		EventID:       "e1",
		RunID:         "r1",
		EventType:     agentobs.EventRunStart,
		Timestamp:     now,
		SchemaVersion: "1.0",
	}); err != nil {
		t.Fatal(err)
	}

	// merge session.start
	if err := s.Merge(&agentobs.AgentEvent{
		EventID:       "e2",
		RunID:         "r1",
		SessionID:     "s1",
		EventType:     agentobs.EventSessionStart,
		Timestamp:     now.Add(time.Second),
		SchemaVersion: "1.0",
		AgentName:     "test-agent",
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "snap.json")
	if err := Save(path, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.RunCount() != 1 {
		t.Fatalf("want 1 run, got %d", loaded.RunCount())
	}
	if loaded.SessionCount() != 1 {
		t.Fatalf("want 1 session, got %d", loaded.SessionCount())
	}

	run, ok := loaded.GetRun("r1")
	if !ok {
		t.Fatal("run r1 not found after load")
	}
	if run.RootSessionID != "s1" {
		t.Fatalf("want root session s1, got %q", run.RootSessionID)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/snap.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrSnapshotMissing) {
		t.Fatalf("want ErrSnapshotMissing, got %v", err)
	}
}

func TestLoad_FallbackToBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	// save a valid snapshot
	s := store.New()
	s.Merge(&agentobs.AgentEvent{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	})
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}

	// save again to create .bak from the first save
	s.Merge(&agentobs.AgentEvent{
		EventID: "e2", RunID: "r1", SessionID: "s1",
		EventType: agentobs.EventSessionStart, Timestamp: time.Now(),
		SchemaVersion: "1.0", AgentName: "a",
	})
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}

	// corrupt primary, forcing fallback to .bak
	os.WriteFile(path, []byte("corrupted"), 0644)

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load should fall back to .bak: %v", err)
	}
	// .bak has only 1 run, no session (from the first save)
	if loaded.RunCount() != 1 {
		t.Fatalf("want 1 run from .bak, got %d", loaded.RunCount())
	}
}

func TestLoad_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	s := store.New()
	s.Merge(&agentobs.AgentEvent{
		EventID: "e1", RunID: "r1", EventType: agentobs.EventRunStart,
		Timestamp: time.Now(), SchemaVersion: "1.0",
	})
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}

	// tamper with the file: replace checksum with a bad one
	raw, _ := os.ReadFile(path)
	tampered := strings.Replace(string(raw), `"checksum":`, `"checksum": "0000000000000000000000000000000000000000000000000000000000000000", "old_checksum":`, 1)
	os.WriteFile(path, []byte(tampered), 0644)

	// remove .bak so there's no fallback
	os.Remove(path + ".bak")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
}

func TestLoad_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	// write a snapshot with wrong version
	os.WriteFile(path, []byte(`{"version":"99","saved_at":"2026-01-01T00:00:00Z","checksum":"abc","data":{}}`), 0644)
	os.Remove(path + ".bak")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for version mismatch")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("want version error, got %v", err)
	}
}
