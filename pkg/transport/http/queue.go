package transporthttp

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type queuedEvent struct {
	ev   *eventv2.Event
	size int64
}

type queue struct {
	cfg     Config
	flushFn func(batch []*eventv2.Event)

	mu        sync.Mutex
	okQ       []queuedEvent
	prioQ     []queuedEvent
	okBytes   int64
	prioBytes int64
	closing   bool
	notify    chan struct{}
	stop      chan struct{}
	drained   chan struct{}
}

func newQueue(cfg Config, flushFn func(batch []*eventv2.Event)) *queue {
	return &queue{
		cfg:     cfg,
		flushFn: flushFn,
		notify:  make(chan struct{}, 1),
		stop:    make(chan struct{}),
		drained: make(chan struct{}),
	}
}

func (q *queue) enqueue(ev *eventv2.Event) bool {
	if ev == nil {
		return false
	}
	size := estimateEventSize(ev)

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closing {
		return false
	}

	accepted := false
	switch ev.Status {
	case eventv2.StatusOK, eventv2.StatusSuppressed:
		accepted = q.enqueueOKLocked(ev, size)
	default:
		accepted = q.enqueuePriorityLocked(ev, size)
	}
	if accepted {
		q.signal()
	}
	return accepted
}

func (q *queue) enqueueOKLocked(ev *eventv2.Event, size int64) bool {
	okCap := q.cfg.InFlightCap * int64(q.cfg.OkBudgetPct) / 100
	if size > okCap {
		return false
	}
	for q.okBytes+size > okCap && len(q.okQ) > 0 {
		q.okBytes -= q.okQ[0].size
		q.okQ = q.okQ[1:]
	}
	if q.okBytes+size > okCap {
		return false
	}
	q.okQ = append(q.okQ, queuedEvent{ev: ev, size: size})
	q.okBytes += size
	return true
}

func (q *queue) enqueuePriorityLocked(ev *eventv2.Event, size int64) bool {
	totalCap := q.cfg.InFlightCap
	if size > totalCap {
		return false
	}
	for q.totalBytesLocked()+size > totalCap && len(q.okQ) > 0 {
		q.okBytes -= q.okQ[0].size
		q.okQ = q.okQ[1:]
	}
	for q.totalBytesLocked()+size > totalCap && len(q.prioQ) > 0 {
		q.prioBytes -= q.prioQ[0].size
		q.prioQ = q.prioQ[1:]
	}
	if q.totalBytesLocked()+size > totalCap {
		return false
	}
	q.prioQ = append(q.prioQ, queuedEvent{ev: ev, size: size})
	q.prioBytes += size
	return true
}

func (q *queue) totalBytesLocked() int64 {
	return q.okBytes + q.prioBytes
}

func (q *queue) run() {
	ticker := time.NewTicker(time.Duration(q.cfg.BatchAgeMs) * time.Millisecond)
	defer ticker.Stop()
	defer close(q.drained)

	for {
		select {
		case <-q.notify:
			for _, batch := range q.takeReadyBatches(false) {
				q.flushFn(batch)
			}
		case <-ticker.C:
			for _, batch := range q.takeReadyBatches(true) {
				q.flushFn(batch)
			}
		case <-q.stop:
			for _, batch := range q.takeReadyBatches(true) {
				q.flushFn(batch)
			}
			return
		}
	}
}

func (q *queue) takeReadyBatches(force bool) [][]*eventv2.Event {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out [][]*eventv2.Event
	for {
		batch := q.takeOneLocked(force)
		if len(batch) == 0 {
			return out
		}
		out = append(out, batch)
	}
}

func (q *queue) takeOneLocked(force bool) []*eventv2.Event {
	if batch := q.takeFromLocked(&q.prioQ, &q.prioBytes, force); len(batch) > 0 {
		return batch
	}
	if batch := q.takeFromLocked(&q.okQ, &q.okBytes, force); len(batch) > 0 {
		return batch
	}
	return nil
}

func (q *queue) takeFromLocked(src *[]queuedEvent, totalBytes *int64, force bool) []*eventv2.Event {
	if len(*src) == 0 {
		return nil
	}
	if !force && len(*src) < q.cfg.MaxBatch && *totalBytes < int64(q.cfg.MaxBatchSize) {
		return nil
	}

	var batch []*eventv2.Event
	var batchBytes int64
	n := 0
	for n < len(*src) && n < q.cfg.MaxBatch {
		next := (*src)[n]
		if n > 0 && batchBytes+next.size > int64(q.cfg.MaxBatchSize) {
			break
		}
		batch = append(batch, next.ev)
		batchBytes += next.size
		n++
	}
	*src = (*src)[n:]
	*totalBytes -= batchBytes
	return batch
}

func (q *queue) shutdown(timeout time.Duration) {
	q.mu.Lock()
	if q.closing {
		q.mu.Unlock()
	} else {
		q.closing = true
		close(q.stop)
		q.mu.Unlock()
	}

	if timeout <= 0 {
		<-q.drained
		return
	}

	select {
	case <-q.drained:
	case <-time.After(timeout):
	}
}

func (q *queue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func estimateEventSize(ev *eventv2.Event) int64 {
	raw, err := json.Marshal(ev)
	if err != nil {
		return 0
	}
	return int64(len(bytes.TrimSpace(raw)) + 1)
}
