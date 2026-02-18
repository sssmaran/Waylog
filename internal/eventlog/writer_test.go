package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/pkg/event"
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
		if err := w.Write(&ev); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Read back and verify 3 valid JSON lines
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
		var ev event.WideEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 lines, got %d", count)
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
			if err := w.Write(&ev); err != nil {
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
		var ev event.WideEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
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
