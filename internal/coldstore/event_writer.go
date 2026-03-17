package coldstore

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type BatchWriterConfig struct {
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
}

type BatchWriter struct {
	store     *Store
	ch        chan event.WideEvent
	done      chan struct{}
	cfg       BatchWriterConfig
	metrics   *metrics.Metrics
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewBatchWriter(store *Store, cfg BatchWriterConfig, m *metrics.Metrics) *BatchWriter {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 10000
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 500 * time.Millisecond
	}
	return &BatchWriter{
		store:   store,
		ch:      make(chan event.WideEvent, cfg.QueueSize),
		done:    make(chan struct{}),
		cfg:     cfg,
		metrics: m,
	}
}

func (bw *BatchWriter) Enqueue(ev event.WideEvent) bool {
	select {
	case bw.ch <- ev:
		return true
	default:
		if bw.metrics != nil {
			bw.metrics.ColdEventsDropped.Inc()
		}
		return false
	}
}

func (bw *BatchWriter) Start() {
	bw.startOnce.Do(func() { go bw.loop() })
}

func (bw *BatchWriter) Stop() {
	bw.stopOnce.Do(func() { close(bw.ch) })
	select {
	case <-bw.done:
	case <-time.After(10 * time.Second):
		slog.Warn("coldstore: drain timeout, some events may be lost")
	}
}

func (bw *BatchWriter) loop() {
	defer close(bw.done)

	batch := make([]event.WideEvent, 0, bw.cfg.BatchSize)
	ticker := time.NewTicker(bw.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case ev, ok := <-bw.ch:
			if !ok {
				// Drain remaining buffered items from channel.
				for ev := range bw.ch {
					batch = append(batch, ev)
					if len(batch) >= bw.cfg.BatchSize {
						bw.flush(batch)
						batch = batch[:0]
					}
				}
				if len(batch) > 0 {
					bw.flush(batch)
				}
				return
			}
			batch = append(batch, ev)
			if len(batch) >= bw.cfg.BatchSize {
				bw.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				bw.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// cols per row in the INSERT (ingested_at uses DB default).
const insertCols = 17

// maxRowsPerInsert keeps total placeholders under SQLite's 999-parameter limit.
const maxRowsPerInsert = 999 / insertCols // 58

const rowPlaceholder = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

func (bw *BatchWriter) flush(batch []event.WideEvent) {
	start := time.Now()

	tx, err := bw.store.writer.Begin()
	if err != nil {
		slog.Error("coldstore: begin tx", "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	for off := 0; off < len(batch); off += maxRowsPerInsert {
		end := off + maxRowsPerInsert
		if end > len(batch) {
			end = len(batch)
		}
		chunk := batch[off:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*insertCols)

		for i, ev := range chunk {
			placeholders[i] = rowPlaceholder

			var errorCode, errorMsg *string
			if ev.Error != nil {
				errorCode = &ev.Error.Code
				errorMsg = &ev.Error.Message
			}

			successInt := 0
			if ev.Outcome.Success {
				successInt = 1
			}

			args = append(args,
				ev.Request.TraceID,
				nilIfEmpty(ev.Request.SpanID),
				nilIfEmpty(ev.Request.ParentSpanID),
				ev.EventName,
				ev.System.Service,
				ev.System.Env,
				nilIfEmpty(ev.System.Version),
				nilIfEmpty(ev.System.DeploymentID),
				ev.User.ID,
				nilIfEmpty(ev.User.Tier),
				nilIfEmpty(ev.Request.Flow),
				ev.Outcome.StatusCode,
				successInt,
				errorCode,
				errorMsg,
				ev.Metrics.LatencyMs,
				ev.Timestamp.UTC().Format(tsFormat),
			)
		}

		query := fmt.Sprintf(`INSERT INTO events (
			trace_id, span_id, parent_span_id, event_name,
			service, env, version, deployment_id,
			user_id, user_tier, flow,
			status_code, success, error_code, error_message,
			latency_ms, timestamp
		) VALUES %s`, strings.Join(placeholders, ", "))

		if _, err := tx.Exec(query, args...); err != nil {
			slog.Error("coldstore: batch insert", "err", err, "count", len(chunk))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("coldstore: commit", "err", err)
		return
	}

	if bw.metrics != nil {
		bw.metrics.ColdEventsWritten.Add(float64(len(batch)))
		bw.metrics.ColdBatchLatency.Observe(time.Since(start).Seconds())
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
