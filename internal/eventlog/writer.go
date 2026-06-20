package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// maxBatch bounds how many queued writes one commit cycle coalesces, capping
// per-batch latency and memory while still amortizing fsync across many writes.
const maxBatch = 256

// errClosed is returned by Write after the writer has been closed.
var errClosed = errors.New("eventlog: writer closed")

// Writer appends WideEvents as JSONL to a file.
//
// A single commit goroutine owns the file, so concurrent Write calls hand their
// entry to it and block until durable. In sync mode the goroutine fsyncs ONCE
// per batch (group commit): N writes that arrive while a prior fsync is in
// flight are made durable by a single fsync, giving WAL durability at far higher
// throughput than a per-write fsync. When SyncOnWrite is false, writes are
// durable only at the process level (OS page cache), as before.
type Writer struct {
	dir          string
	syncOnWrite  bool
	maxFileBytes int64

	reqCh chan *writeReq
	quit  chan struct{}
	done  chan struct{}

	// File state is owned exclusively by the commit goroutine. activePath is
	// mirrored under pathMu so ActivePath can be read concurrently.
	f            *os.File
	enc          *json.Encoder
	bytesWritten int64
	seq          int // disambiguator for same-second rotation

	pathMu     sync.Mutex
	activePath string

	syncCount atomic.Int64 // observable fsync count (test/metric)
	closeOnce sync.Once
	closeErr  error
}

type writeReq struct {
	entry LogEntry
	ack   chan error
}

// New creates a Writer that appends to a new JSONL file in dir.
// Writes are buffered in the OS page cache (no per-write fsync).
func New(dir string) (*Writer, error) {
	return NewWithConfig(dir, WriterConfig{})
}

// NewWithSync creates a Writer with per-batch fsync for strict WAL durability.
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
		reqCh:        make(chan *writeReq, maxBatch),
		quit:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	if err := w.openNewFile(); err != nil {
		return nil, err
	}
	go w.commitLoop()
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
	w.pathMu.Lock()
	w.activePath = path
	w.pathMu.Unlock()
	return nil
}

// Write appends a single event wrapped in a LogEntry. Safe for concurrent use.
// It blocks until the entry is durable (fsynced in sync mode, encoded otherwise).
func (w *Writer) Write(ev *event.WideEvent, sampledInGraph bool) error {
	req := &writeReq{
		entry: LogEntry{LoggedAt: time.Now().UTC(), Event: *ev, SampledInGraph: sampledInGraph},
		ack:   make(chan error, 1),
	}
	select {
	case w.reqCh <- req:
	case <-w.done:
		return errClosed
	}
	select {
	case err := <-req.ack:
		return err
	case <-w.done:
		return errClosed
	}
}

// commitLoop is the single owner of the file. It batches queued writes, encodes
// them, fsyncs once per batch (in sync mode), then acks every waiter.
func (w *Writer) commitLoop() {
	defer close(w.done)
	for {
		select {
		case req := <-w.reqCh:
			w.processBatch(req)
		case <-w.quit:
			w.drainAndClose()
			return
		}
	}
}

// processBatch coalesces the leading request plus any already-queued requests
// into one encode+fsync cycle.
func (w *Writer) processBatch(first *writeReq) {
	batch := []*writeReq{first}
	for len(batch) < maxBatch {
		select {
		case req := <-w.reqCh:
			batch = append(batch, req)
		default:
			goto full
		}
	}
full:
	w.commit(batch)
}

// commit encodes every entry, fsyncs once when in sync mode, handles rotation,
// then acks all waiters with the batch result.
func (w *Writer) commit(batch []*writeReq) {
	var err error
	for _, req := range batch {
		if err = w.enc.Encode(req.entry); err != nil {
			break
		}
	}
	if err == nil && w.syncOnWrite {
		w.syncCount.Add(1)
		err = w.f.Sync()
	}
	if err == nil {
		err = w.maybeRotate()
	}
	for _, req := range batch {
		req.ack <- err
	}
}

// maybeRotate rolls to a new file once the active file reaches the size limit.
func (w *Writer) maybeRotate() error {
	if w.maxFileBytes <= 0 {
		return nil
	}
	info, statErr := w.f.Stat()
	if statErr != nil {
		return nil // best-effort: skip rotation if we can't size the file
	}
	w.bytesWritten = info.Size()
	if w.bytesWritten < w.maxFileBytes {
		return nil
	}
	_ = w.f.Sync()
	_ = w.f.Close()
	return w.openNewFile()
}

// drainAndclose processes any still-queued writes, then fsyncs and closes.
func (w *Writer) drainAndClose() {
	for {
		select {
		case req := <-w.reqCh:
			w.commit([]*writeReq{req})
		default:
			w.closeErr = w.closeFile()
			return
		}
	}
}

func (w *Writer) closeFile() error {
	syncErr := w.f.Sync()
	closeErr := w.f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// ActivePath returns the path of the currently active log file.
func (w *Writer) ActivePath() string {
	w.pathMu.Lock()
	defer w.pathMu.Unlock()
	return w.activePath
}

// Close flushes and closes the underlying file, stopping the commit goroutine.
func (w *Writer) Close() error {
	w.closeOnce.Do(func() { close(w.quit) })
	<-w.done
	return w.closeErr
}
