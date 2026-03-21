package transport

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type InMemoryTransport struct {
	mu     sync.Mutex
	events []event.WideEvent
	closed atomic.Bool
}

func NewInMemoryTransport() *InMemoryTransport {
	return &InMemoryTransport{}
}

func (t *InMemoryTransport) Send(ctx context.Context, batch []event.WideEvent) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.closed.Load() {
		return 0, errTransportClosed
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed.Load() {
		return 0, errTransportClosed
	}

	if len(batch) == 0 {
		return 0, nil
	}

	copyBatch := make([]event.WideEvent, len(batch))
	copy(copyBatch, batch)
	t.events = append(t.events, copyBatch...)
	return len(batch), nil
}

func (t *InMemoryTransport) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.closed.CompareAndSwap(false, true) {
		return nil
	}
	return nil
}

func (t *InMemoryTransport) Events() []event.WideEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	copyEvents := make([]event.WideEvent, len(t.events))
	copy(copyEvents, t.events)
	return copyEvents
}
