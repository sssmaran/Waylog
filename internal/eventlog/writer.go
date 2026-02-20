package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// LogEntry wraps a WideEvent with ingest-time metadata for reliable replay.
type LogEntry struct {
	LoggedAt       time.Time       `json:"logged_at"`
	Event          event.WideEvent `json:"event"`
	SampledInGraph bool            `json:"sampled_in_graph"`
}

// Writer appends WideEvents as JSONL to a file.
//
// When syncOnWrite is true, every Write call fsyncs to disk before returning,
// giving true WAL durability across host/power crashes. When false (default),
// writes are durable only at the process level — the OS page cache may lose
// data on a hard crash, but process-level failures are safe because the handler
// rejects events whose Write fails.
type Writer struct {
	mu          sync.Mutex
	f           *os.File
	enc         *json.Encoder
	syncOnWrite bool
}

// New creates a Writer that appends to a new JSONL file in dir.
// Writes are buffered in the OS page cache (no per-write fsync).
func New(dir string) (*Writer, error) {
	return newWriter(dir, false)
}

// NewWithSync creates a Writer with per-write fsync for strict WAL durability.
// Each Write call fsyncs to disk before returning, which is slower but
// survives host/power crashes.
func NewWithSync(dir string) (*Writer, error) {
	return newWriter(dir, true)
}

func newWriter(dir string, syncOnWrite bool) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir %s: %w", dir, err)
	}
	name := fmt.Sprintf("events-%s.jsonl", time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	return &Writer{f: f, enc: json.NewEncoder(f), syncOnWrite: syncOnWrite}, nil
}

// Write appends a single event wrapped in a LogEntry. Safe for concurrent use.
// If syncOnWrite is enabled, fsyncs to disk before returning.
func (w *Writer) Write(ev *event.WideEvent, sampledInGraph bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(LogEntry{
		LoggedAt:       time.Now().UTC(),
		Event:          *ev,
		SampledInGraph: sampledInGraph,
	}); err != nil {
		return err
	}
	if w.syncOnWrite {
		return w.f.Sync()
	}
	return nil
}

// Close flushes and closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	syncErr := w.f.Sync()
	closeErr := w.f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
