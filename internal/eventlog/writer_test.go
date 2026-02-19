package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestWriter_Write(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	for i := 0; i < 3; i++ {
		ev := testutil.MakeEvent()
		if err := w.Write(&ev, true); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Read back and verify 3 valid LogEntry JSON lines
	entries, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d (err=%v)", len(entries), err)
	}

	f, err := os.Open(entries[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", count, err)
		}
		if entry.LoggedAt.IsZero() {
			t.Fatalf("line %d: logged_at is zero", count)
		}
		if entry.Event.SchemaVersion == "" {
			t.Fatalf("line %d: event missing schema_version", count)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 lines, got %d", count)
	}
}

func TestWriter_SampledInGraphFlag(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	ev := testutil.MakeEvent()
	w.Write(&ev, true)
	w.Write(&ev, false)

	files, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	f, _ := os.Open(files[0])
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var flags []bool
	for scanner.Scan() {
		var entry LogEntry
		json.Unmarshal(scanner.Bytes(), &entry)
		flags = append(flags, entry.SampledInGraph)
	}
	if len(flags) != 2 || flags[0] != true || flags[1] != false {
		t.Fatalf("expected [true, false], got %v", flags)
	}
}

func TestWriter_ConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev := testutil.MakeEvent()
			if err := w.Write(&ev, true); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	wg.Wait()

	entries, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	f, err := os.Open(entries[0])
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 10 {
		t.Fatalf("expected 10 lines, got %d", count)
	}
}

func TestWriter_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "deep")
	w, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}
