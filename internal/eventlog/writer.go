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

// Writer appends WideEvents as JSONL to a file.
type Writer struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// New creates a Writer that appends to a new JSONL file in dir.
// The directory is created if it does not exist.
func New(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir %s: %w", dir, err)
	}
	name := fmt.Sprintf("events-%s.jsonl", time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	return &Writer{f: f, enc: json.NewEncoder(f)}, nil
}

// Write appends a single event as a JSON line. Safe for concurrent use.
func (w *Writer) Write(ev *event.WideEvent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enc.Encode(ev)
}

// Close flushes and closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
