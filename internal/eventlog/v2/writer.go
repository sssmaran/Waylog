// Package eventlogv2 writes schema-2.0 ingest WAL entries as raw JSONL.
package eventlogv2

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Writer struct {
	mu           sync.Mutex
	dir          string
	syncOnWrite  bool
	maxFileBytes int64
	f            *os.File
	activePath   string
	bytesWritten int64
	seq          int
}

type Option func(*Writer)

func WithSync(enabled bool) Option {
	return func(w *Writer) { w.syncOnWrite = enabled }
}

func WithMaxBytes(n int64) Option {
	return func(w *Writer) { w.maxFileBytes = n }
}

func New(dir string, opts ...Option) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("eventlogv2: mkdir %s: %w", dir, err)
	}
	w := &Writer{dir: dir}
	for _, opt := range opts {
		opt(w)
	}
	if err := w.openNewFileLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) WriteRaw(line []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return fmt.Errorf("eventlogv2: writer closed")
	}
	line = bytes.TrimRight(line, "\r\n")
	record := make([]byte, 0, len(line)+1)
	record = append(record, line...)
	record = append(record, '\n')
	written, err := w.f.Write(record)
	if err != nil {
		return err
	}
	w.bytesWritten += int64(written)
	if w.syncOnWrite {
		if err := w.f.Sync(); err != nil {
			return err
		}
	}
	if w.maxFileBytes > 0 && w.bytesWritten >= w.maxFileBytes {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) ActivePath() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.activePath
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	w.activePath = ""
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (w *Writer) rotateLocked() error {
	if w.f != nil {
		_ = w.f.Sync()
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
		w.activePath = ""
	}
	return w.openNewFileLocked()
}

func (w *Writer) openNewFileLocked() error {
	ts := time.Now().UTC().Format("20060102-150405")
	for {
		name := fmt.Sprintf("events-%s.jsonl", ts)
		if w.seq > 0 {
			name = fmt.Sprintf("events-%s-%d.jsonl", ts, w.seq)
		}
		path := filepath.Join(w.dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644)
		if os.IsExist(err) {
			w.seq++
			continue
		}
		if err != nil {
			return fmt.Errorf("eventlogv2: open %s: %w", path, err)
		}
		w.f = f
		w.activePath = path
		w.bytesWritten = 0
		return nil
	}
}
