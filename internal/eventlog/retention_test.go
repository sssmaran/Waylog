package eventlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneOlderThan_DeletesOldFiles(t *testing.T) {
	dir := t.TempDir()

	// Create files with timestamps: 2 old, 1 recent.
	oldNames := []string{
		"events-20250101-000000.jsonl",
		"events-20250102-000000.jsonl",
	}
	recentName := "events-" + time.Now().UTC().Format("20060102-150405") + ".jsonl"

	for _, name := range append(oldNames, recentName) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := PruneOlderThan(dir, 24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	// Recent file should still exist.
	if _, err := os.Stat(filepath.Join(dir, recentName)); os.IsNotExist(err) {
		t.Error("recent file was deleted")
	}
}

func TestPruneOlderThan_SkipsActiveFile(t *testing.T) {
	dir := t.TempDir()

	oldName := "events-20250101-000000.jsonl"
	activePath := filepath.Join(dir, oldName)

	if err := os.WriteFile(activePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	deleted, err := PruneOlderThan(dir, 24*time.Hour, activePath)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0 (active file should be skipped)", deleted)
	}
	if _, err := os.Stat(activePath); os.IsNotExist(err) {
		t.Error("active file was deleted")
	}
}

func TestPruneOlderThan_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	deleted, err := PruneOlderThan(dir, 24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestParseFileTimestamp(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"events-20260220-091300.jsonl", true},
		{"events-baddate.jsonl", false},
		{"notanevent.jsonl", false},
	}
	for _, tt := range tests {
		_, ok := parseFileTimestamp(tt.name)
		if ok != tt.ok {
			t.Errorf("parseFileTimestamp(%q) ok = %v, want %v", tt.name, ok, tt.ok)
		}
	}
}
