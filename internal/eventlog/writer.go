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

// WriterConfig holds configuration for event log writer.
type WriterConfig struct {
	SyncOnWrite  bool
	MaxFileBytes int64 // 0 = no rotation
}

// Writer appends WideEvents as JSONL to a file.
//
// When syncOnWrite is true, every Write call fsyncs to disk before returning,
// giving true WAL durability across host/power crashes. When false (default),
// writes are durable only at the process level — the OS page cache may lose
// data on a hard crash, but process-level failures are safe because the handler
// rejects events whose Write fails.
type Writer struct {
	mu           sync.Mutex
	dir          string
	f            *os.File
	enc          *json.Encoder
	syncOnWrite  bool
	maxFileBytes int64
	bytesWritten int64
	seq          int // disambiguator for same-second rotation
}

// New creates a Writer that appends to a new JSONL file in dir.
// Writes are buffered in the OS page cache (no per-write fsync).
func New(dir string) (*Writer, error) {
	return NewWithConfig(dir, WriterConfig{})
}

// NewWithSync creates a Writer with per-write fsync for strict WAL durability.
func NewWithSync(dir string) (*Writer, error) {
	return NewWithConfig(dir, WriterConfig{SyncOnWrite: true})
}

// NewWithConfig creates a Writer with the given configuration.
func NewWithConfig(dir string, cfg WriterConfig) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir %s: %w", dir, err)
	}
	w := &Writer{
		dir:          dir,
		syncOnWrite:  cfg.SyncOnWrite,
		maxFileBytes: cfg.MaxFileBytes,
	}
	if err := w.openNewFile(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) openNewFile() error {
	ts := time.Now().UTC().Format("20060102-150405")
	name := fmt.Sprintf("events-%s.jsonl", ts)
	path := filepath.Join(w.dir, name)
	// If file already exists (same-second rotation), add sequence suffix.
	if _, err := os.Stat(path); err == nil {
		w.seq++
		name = fmt.Sprintf("events-%s-%d.jsonl", ts, w.seq)
		path = filepath.Join(w.dir, name)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	w.f = f
	w.enc = json.NewEncoder(f)
	w.bytesWritten = 0
	return nil
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
		if err := w.f.Sync(); err != nil {
			return err
		}
	}
	// Track bytes written for rotation decisions.
	if info, err := w.f.Stat(); err == nil {
		w.bytesWritten = info.Size()
	}
	// Rotate if file exceeds size limit.
	if w.maxFileBytes > 0 && w.bytesWritten >= w.maxFileBytes {
		_ = w.f.Sync()
		_ = w.f.Close()
		if err := w.openNewFile(); err != nil {
			return err
		}
	}
	return nil
}

// ActivePath returns the path of the currently active log file.
func (w *Writer) ActivePath() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Name()
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
