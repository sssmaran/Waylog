package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

// LogEntry wraps an event with ingest metadata.
type LogEntry struct {
	LoggedAt time.Time           `json:"logged_at"`
	Event    agentobs.AgentEvent `json:"event"`
}

// WriterConfig controls writer behavior.
type WriterConfig struct {
	SyncOnWrite  bool
	MaxFileBytes int64
}

// Writer appends agent events to JSONL files.
type Writer struct {
	mu           sync.Mutex
	dir          string
	cfg          WriterConfig
	f            *os.File
	enc          *json.Encoder
	bytesWritten int64
}

// New creates a writer without fsync.
func New(dir string) (*Writer, error) {
	return NewWithConfig(dir, WriterConfig{})
}

// NewWithSync creates a writer with per-write fsync.
func NewWithSync(dir string) (*Writer, error) {
	return NewWithConfig(dir, WriterConfig{SyncOnWrite: true})
}

// NewWithConfig creates a writer with full configuration.
func NewWithConfig(dir string, cfg WriterConfig) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	w := &Writer{dir: dir, cfg: cfg}
	if err := w.openNewFile(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) openNewFile() error {
	name := fmt.Sprintf("agent-events-%s.jsonl", time.Now().Format("20060102-150405"))
	path := filepath.Join(w.dir, name)

	// Handle same-second collisions.
	for n := 1; fileExists(path); n++ {
		name = fmt.Sprintf("agent-events-%s-%d.jsonl", time.Now().Format("20060102-150405"), n)
		path = filepath.Join(w.dir, name)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	w.f = f
	w.enc = json.NewEncoder(f)
	w.bytesWritten = 0
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Write appends an event as a JSONL line, rotating if needed.
func (w *Writer) Write(ev *agentobs.AgentEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := LogEntry{
		LoggedAt: time.Now(),
		Event:    *ev,
	}

	if err := w.enc.Encode(entry); err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	if w.cfg.SyncOnWrite {
		if err := w.f.Sync(); err != nil {
			return fmt.Errorf("sync: %w", err)
		}
	}

	info, err := w.f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	w.bytesWritten = info.Size()

	if w.cfg.MaxFileBytes > 0 && w.bytesWritten >= w.cfg.MaxFileBytes {
		if err := w.f.Close(); err != nil {
			return fmt.Errorf("close on rotate: %w", err)
		}
		if err := w.openNewFile(); err != nil {
			return err
		}
	}

	return nil
}

// Close syncs and closes the current file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		return nil
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return w.f.Close()
}
