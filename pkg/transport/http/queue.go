package transporthttp

import (
	"encoding/json"
	"sync"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type queuedEvent struct {
	ev       *eventv2.Event
	size     int64
	attempts int
}

type queue struct {
	cfg     Config
	flushFn func(batch []*eventv2.Event) deliveryResult
	dropFn  func(n int)

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

type batchClass uint8

const (
	classOK batchClass = iota
	classPriority
)

type queuedBatch struct {
	class  batchClass
	events []queuedEvent
}

func newQueue(cfg Config, flushFn func(batch []*eventv2.Event) deliveryResult, dropFn func(n int)) *queue {
	return &queue{
		cfg:     cfg,
		flushFn: flushFn,
		dropFn:  dropFn,
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
	if ev.Status.IsPriority() {
		accepted = q.enqueuePriorityLocked(ev, size)
	} else {
		accepted = q.enqueueOKLocked(ev, size)
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
		q.dropOKLocked()
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
		q.dropOKLocked()
	}
	for q.totalBytesLocked()+size > totalCap && len(q.prioQ) > 0 {
		q.dropPriorityLocked()
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
				q.deliver(batch)
			}
		case <-ticker.C:
			for _, batch := range q.takeReadyBatches(true) {
				q.deliver(batch)
			}
		case <-q.stop:
			for _, batch := range q.takeReadyBatches(true) {
				q.deliver(batch)
			}
			return
		}
	}
}

func (q *queue) takeReadyBatches(force bool) []queuedBatch {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out []queuedBatch
	for {
		batch := q.takeOneLocked(force)
		if len(batch.events) == 0 {
			return out
		}
		out = append(out, batch)
	}
}

func (q *queue) takeOneLocked(force bool) queuedBatch {
	if batch := q.takeFromLocked(&q.prioQ, &q.prioBytes, force); len(batch) > 0 {
		return queuedBatch{class: classPriority, events: batch}
	}
	if batch := q.takeFromLocked(&q.okQ, &q.okBytes, force); len(batch) > 0 {
		return queuedBatch{class: classOK, events: batch}
	}
	return queuedBatch{}
}

func (q *queue) takeFromLocked(src *[]queuedEvent, totalBytes *int64, force bool) []queuedEvent {
	if len(*src) == 0 {
		return nil
	}
	if !force && len(*src) < q.cfg.MaxBatch && *totalBytes < int64(q.cfg.MaxBatchSize) {
		return nil
	}

	var batch []queuedEvent
	var batchBytes int64
	n := 0
	for n < len(*src) && n < q.cfg.MaxBatch {
		next := (*src)[n]
		if n > 0 && batchBytes+next.size > int64(q.cfg.MaxBatchSize) {
			break
		}
		batch = append(batch, next)
		batchBytes += next.size
		n++
	}
	*src = (*src)[n:]
	*totalBytes -= batchBytes
	return batch
}

func (q *queue) deliver(batch queuedBatch) {
	events := make([]*eventv2.Event, 0, len(batch.events))
	for _, item := range batch.events {
		events = append(events, item.ev)
	}

	result := q.flushFn(events)
	if result.success {
		return
	}
	if !result.retryable {
		return
	}

	retry := queuedBatch{class: batch.class}
	for _, ev := range batch.events {
		ev.attempts++
		if ev.attempts > q.cfg.MaxRetries {
			if q.dropFn != nil {
				q.dropFn(1)
			}
			continue
		}
		retry.events = append(retry.events, ev)
	}
	if len(retry.events) == 0 {
		return
	}
	if q.isClosing() {
		if q.dropFn != nil {
			q.dropFn(len(retry.events))
		}
		return
	}

	// A failing endpoint stalls the single delivery loop for the backoff
	// window; switch to time.AfterFunc + signal if retry storms become a
	// throughput problem.
	if result.retryAfter > 0 {
		time.Sleep(result.retryAfter)
	} else {
		time.Sleep(q.backoff(retry.events))
	}
	if q.requeueFront(retry) {
		q.signal()
	} else if q.dropFn != nil {
		q.dropFn(len(retry.events))
	}
}

func (q *queue) backoff(events []queuedEvent) time.Duration {
	maxAttempt := 1
	for _, ev := range events {
		if ev.attempts > maxAttempt {
			maxAttempt = ev.attempts
		}
	}
	d := defaultBackoffMin << (maxAttempt - 1)
	if d > defaultBackoffMax {
		return defaultBackoffMax
	}
	return d
}

func (q *queue) requeueFront(batch queuedBatch) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closing {
		return false
	}

	var bytes int64
	for _, ev := range batch.events {
		bytes += ev.size
	}
	switch batch.class {
	case classPriority:
		totalCap := q.cfg.InFlightCap
		for q.totalBytesLocked()+bytes > totalCap && len(q.okQ) > 0 {
			q.dropOKLocked()
		}
		for q.totalBytesLocked()+bytes > totalCap && len(q.prioQ) > 0 {
			q.dropPriorityTailLocked()
		}
		if q.totalBytesLocked()+bytes > totalCap {
			return false
		}
		q.prioQ = append(append([]queuedEvent(nil), batch.events...), q.prioQ...)
		q.prioBytes += bytes
	default:
		okCap := q.cfg.InFlightCap * int64(q.cfg.OkBudgetPct) / 100
		for q.okBytes+bytes > okCap && len(q.okQ) > 0 {
			q.dropOKLocked()
		}
		if q.okBytes+bytes > okCap {
			return false
		}
		q.okQ = append(append([]queuedEvent(nil), batch.events...), q.okQ...)
		q.okBytes += bytes
	}
	return true
}

func (q *queue) dropOKLocked() {
	q.dropHeadLocked(&q.okQ, &q.okBytes)
}

func (q *queue) dropPriorityLocked() {
	q.dropHeadLocked(&q.prioQ, &q.prioBytes)
}

func (q *queue) dropHeadLocked(src *[]queuedEvent, totalBytes *int64) {
	if len(*src) == 0 {
		return
	}
	*totalBytes -= (*src)[0].size
	*src = (*src)[1:]
	if q.dropFn != nil {
		q.dropFn(1)
	}
}

func (q *queue) dropPriorityTailLocked() {
	if len(q.prioQ) == 0 {
		return
	}
	last := len(q.prioQ) - 1
	q.prioBytes -= q.prioQ[last].size
	q.prioQ = q.prioQ[:last]
	if q.dropFn != nil {
		q.dropFn(1)
	}
}

func (q *queue) isClosing() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closing
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
	return int64(len(raw) + 1)
}
