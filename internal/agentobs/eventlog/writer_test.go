package eventlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

func makeEvent(id string) *agentobs.AgentEvent {
	return &agentobs.AgentEvent{
		EventID:       id,
		RunID:         "run-1",
		EventType:     agentobs.EventRunStart,
		Timestamp:     time.Now(),
		SchemaVersion: "1.0",
	}
}

func TestWriter_WriteAndRead(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ev := makeEvent("evt-001")
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, stats, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if stats.EntriesLoaded != 1 {
		t.Fatalf("expected 1 entry, got %d", stats.EntriesLoaded)
	}
	if entries[0].Event.EventID != "evt-001" {
		t.Fatalf("expected event_id evt-001, got %s", entries[0].Event.EventID)
	}
}

func TestWriter_Rotation(t *testing.T) {
	dir := t.TempDir()

	w, err := NewWithConfig(dir, WriterConfig{MaxFileBytes: 100})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}

	for i := 0; i < 5; i++ {
		ev := makeEvent("rot-" + string(rune('A'+i)))
		if err := w.Write(ev); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected multiple files after rotation, got %d", len(matches))
	}
}

func TestWriter_DedupIndex(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ev := makeEvent("dedup-42")
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, _, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	idx := BuildDedupIndex(entries)
	if !idx["dedup-42"] {
		t.Fatal("expected dedup-42 in index")
	}
	if idx["nonexistent"] {
		t.Fatal("expected nonexistent to be absent")
	}

	// Also verify the file exists on disk
	matches, _ := os.ReadDir(dir)
	if len(matches) == 0 {
		t.Fatal("expected at least one file in dir")
	}
}

func TestReadDir_MissingDir(t *testing.T) {
	_, _, err := ReadDir("/nonexistent/wal/path", time.Time{})
	if err == nil {
		t.Fatal("expected error for missing WAL directory")
	}
}

func TestReadDir_CorruptedLines(t *testing.T) {
	dir := t.TempDir()

	// Write a valid event
	w, _ := New(dir)
	w.Write(makeEvent("good-1"))
	w.Close()

	// Append garbage to the file
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	f, _ := os.OpenFile(matches[0], os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString("this is not valid json\n")
	f.Close()

	// Write another valid event after the garbage
	f2, _ := os.OpenFile(matches[0], os.O_WRONLY|os.O_APPEND, 0o644)
	f2.WriteString(`{"logged_at":"2026-01-01T00:00:00Z","event":{"event_id":"good-2","run_id":"r","event_type":"run.start","timestamp":"2026-01-01T00:00:00Z","schema_version":"1.0"}}` + "\n")
	f2.Close()

	entries, stats, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatalf("ReadDir should succeed with partial recovery: %v", err)
	}
	if stats.LinesCorrupted != 1 {
		t.Fatalf("expected 1 corrupted line, got %d", stats.LinesCorrupted)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 valid entries (partial recovery), got %d", len(entries))
	}
}
