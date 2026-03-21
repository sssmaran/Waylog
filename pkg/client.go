package waylog

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	"github.com/sssmaran/WaylogCLI/pkg/transport"
)

const (
	defaultQueueSize       = 10000
	defaultBatchSize       = 100
	defaultFlushInterval   = time.Second
	defaultShutdownTimeout = 5 * time.Second
)

var (
	defaultClient atomic.Pointer[Client]
	defaultStats  stats
	initMu        sync.Mutex
)

func Init(cfg Config) error {
	initMu.Lock()
	defer initMu.Unlock()

	if defaultClient.Load() != nil {
		return ErrAlreadyInitialized
	}
	client, err := New(cfg)
	if err != nil {
		return err
	}
	defaultClient.Store(client)
	return nil
}

func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	applyDefaults(&cfg)

	t := cfg.Transport
	if t == nil {
		switch {
		case cfg.IngestURL != "":
			ht, err := transport.NewHTTPTransport(cfg.IngestURL, 0)
			if err != nil {
				return nil, err
			}
			t = ht
		default:
			t = &transport.NopTransport{}
		}
	}

	client := &Client{
		cfg:       cfg,
		transport: t,
		queue:     make(chan event.WideEvent, cfg.QueueSize),
		closeCh:   make(chan struct{}),
	}

	client.wg.Add(1)
	go client.run()

	return client, nil
}

func RequestEnd(ctx context.Context) {
	client := defaultClient.Load()
	if client == nil {
		defaultStats.incDropped(1)
		return
	}
	client.RequestEnd(ctx)
}

func Error(ctx context.Context, err error) {
	client := defaultClient.Load()
	if client == nil {
		defaultStats.incDropped(1)
		return
	}
	client.Error(ctx, err)
}

func Stats() StatsSnapshot {
	client := defaultClient.Load()
	if client == nil {
		return defaultStats.snapshot()
	}
	return client.stats.snapshot()
}

func DefaultServiceName() string {
	client := defaultClient.Load()
	if client == nil {
		return ""
	}
	return client.cfg.Service
}

func Shutdown(ctx context.Context) error {
	client := defaultClient.Load()
	if client == nil {
		defaultStats.incDropped(1)
		return nil
	}
	return client.Close(ctx)
}

type Client struct {
	cfg       Config
	transport transport.Transport
	queue     chan event.WideEvent
	stats     stats

	closeOnce   sync.Once
	closeCh     chan struct{}
	closed      atomic.Bool
	wg          sync.WaitGroup
	shutdownCtx atomic.Value
}

func (c *Client) ServiceName() string {
	if c == nil {
		return ""
	}
	return c.cfg.Service
}

// Stats returns a snapshot of this client's counters.
func (c *Client) Stats() StatsSnapshot {
	if c == nil {
		return StatsSnapshot{}
	}
	return c.stats.snapshot()
}

func (c *Client) RequestEnd(ctx context.Context) {
	if c == nil {
		return
	}
	state, ok := requestStateFromContext(ctx)
	if !ok || state == nil {
		c.stats.incDropped(1)
		return
	}

	state.once.Do(func() {
		state.mu.Lock()
		statusCode := state.statusCode
		err := state.err
		callerService := state.callerService
		downstreamService := state.downstreamService
		state.mu.Unlock()

		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		if err != nil && statusCode < http.StatusBadRequest {
			statusCode = http.StatusInternalServerError
		}

		latencyMs := int64(0)
		if !state.start.IsZero() {
			latencyMs = time.Since(state.start).Milliseconds()
		}

		ev := c.assembleEvent(ctx, statusCode, latencyMs, err, callerService, downstreamService)
		if err := ev.Validate(); err != nil {
			c.stats.incValidateFailed(1)
			c.stats.incDropped(1)
			return
		}
		c.enqueue(ev)
	})
}

func (c *Client) Error(ctx context.Context, err error) {
	if c == nil || err == nil {
		return
	}
	state, ok := requestStateFromContext(ctx)
	if !ok || state == nil {
		c.stats.incDropped(1)
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.err != nil {
		return
	}

	state.err = err
	if state.statusCode < http.StatusBadRequest {
		state.statusCode = http.StatusInternalServerError
	}
}

func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var closeErr error

	c.closeOnce.Do(func() {
		shutdownCtx, cancel := ensureTimeout(ctx, c.cfg.ShutdownTimeout)
		c.shutdownCtx.Store(shutdownCtx)

		c.closed.Store(true)
		close(c.closeCh)
		close(c.queue)

		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			closeErr = c.transport.Close(shutdownCtx)
		case <-shutdownCtx.Done():
			closeErr = shutdownCtx.Err()
		}

		cancel()
	})

	return closeErr
}

func (c *Client) enqueue(ev event.WideEvent) {
	if c.closed.Load() {
		c.stats.incDropped(1)
		return
	}

	defer func() {
		if recover() != nil {
			c.stats.incDropped(1)
		}
	}()

	select {
	case c.queue <- ev:
		return
	default:
		c.stats.incDropped(1)
	}
}

func (c *Client) run() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]event.WideEvent, 0, c.cfg.BatchSize)

	flush := func(ctx context.Context) {
		if len(batch) == 0 {
			return
		}
		sent, err := c.transport.Send(ctx, batch)
		if sent > 0 {
			c.stats.incEmitted(uint64(sent))
		}
		if err != nil {
			c.stats.incTransportErrors(1)
			dropped := len(batch) - sent
			if dropped > 0 {
				c.stats.incDropped(uint64(dropped))
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case ev, ok := <-c.queue:
			if !ok {
				shutdownCtx := c.shutdownContext()
				flush(shutdownCtx)
				return
			}
			batch = append(batch, ev)
			if len(batch) >= c.cfg.BatchSize {
				flush(context.Background())
			}
		case <-ticker.C:
			flush(context.Background())
		case <-c.closeCh:
			shutdownCtx := c.shutdownContext()
			for {
				select {
				case ev, ok := <-c.queue:
					if !ok {
						flush(shutdownCtx)
						return
					}
					batch = append(batch, ev)
					if len(batch) >= c.cfg.BatchSize {
						flush(shutdownCtx)
					}
				case <-shutdownCtx.Done():
					dropped := len(batch) + drainQueue(c.queue)
					if dropped > 0 {
						c.stats.incDropped(uint64(dropped))
					}
					return
				}
			}
		}
	}
}

func (c *Client) shutdownContext() context.Context {
	if ctx, ok := c.shutdownCtx.Load().(context.Context); ok && ctx != nil {
		return ctx
	}
	return context.Background()
}

func drainQueue(queue <-chan event.WideEvent) int {
	dropped := 0
	for {
		select {
		case _, ok := <-queue:
			if !ok {
				return dropped
			}
			dropped++
		default:
			return dropped
		}
	}
}

func ensureTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	if ctx == nil {
		return context.WithTimeout(context.Background(), timeout)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) <= timeout {
			return context.WithCancel(ctx)
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func applyDefaults(cfg *Config) {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultFlushInterval
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}
}
