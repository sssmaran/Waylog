package agentobs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type Client struct {
	cfg       clientConfig
	transport *transport
	queue     chan any
	closeCh   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	sent      atomic.Int64
	dropped   atomic.Int64
	txErrors  atomic.Int64

	mu       sync.Mutex
	sessions []*Session
}

func NewClient(baseURL string, opts ...ClientOption) *Client {
	cfg := clientConfig{
		flushInterval: time.Second,
		batchSize:     100,
		queueSize:     10000,
		maxRetries:    3,
	}
	for _, o := range opts {
		o(&cfg)
	}
	c := &Client{
		cfg:       cfg,
		transport: newTransport(baseURL, cfg.apiKey),
		queue:     make(chan any, cfg.queueSize),
		closeCh:   make(chan struct{}),
	}
	c.wg.Add(1)
	go c.run()
	return c
}

func (c *Client) trackSession(s *Session) {
	c.mu.Lock()
	c.sessions = append(c.sessions, s)
	c.mu.Unlock()
}

func (c *Client) cancelAllSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s.hbCancel != nil {
			s.hbCancel()
		}
	}
	c.sessions = nil
}

func (c *Client) run() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.flushInterval)
	defer ticker.Stop()
	var batch []any

	flush := func() {
		if len(batch) == 0 {
			return
		}
		var lastErr error
		maxAttempts := c.cfg.maxRetries + 1
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		for attempt := 0; attempt < maxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			lastErr = c.transport.send(ctx, batch)
			cancel()
			if lastErr == nil {
				c.sent.Add(int64(len(batch)))
				break
			}
			var te *TransportError
			if !errors.As(lastErr, &te) || !te.Retryable {
				break
			}
			if attempt < maxAttempts-1 {
				time.Sleep(time.Duration(100<<uint(attempt)) * time.Millisecond)
			}
		}
		if lastErr != nil {
			c.txErrors.Add(1)
			if c.cfg.errorHandler != nil {
				c.cfg.errorHandler(DeliveryError{Err: lastErr})
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case ev := <-c.queue:
			batch = append(batch, ev)
			if len(batch) >= c.cfg.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-c.closeCh:
			for {
				select {
				case ev := <-c.queue:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (c *Client) emit(ev any) {
	select {
	case c.queue <- ev:
	default:
		c.dropped.Add(1)
		if c.cfg.errorHandler != nil {
			c.cfg.errorHandler(DeliveryError{Err: fmt.Errorf("queue full")})
		}
	}
}

func (c *Client) Close(ctx context.Context) error {
	c.cancelAllSessions()
	c.closeOnce.Do(func() { close(c.closeCh) })
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Stats() ClientStats {
	return ClientStats{
		EventsSent:      c.sent.Load(),
		Dropped:         c.dropped.Load(),
		TransportErrors: c.txErrors.Load(),
	}
}

func (c *Client) StartRun(ctx context.Context, name string) *Run {
	runID := uuid.New().String()
	r := &Run{client: c, runID: runID}
	ev := map[string]any{
		"event_id":       uuid.New().String(),
		"run_id":         runID,
		"event_type":     "run.start",
		"timestamp":      time.Now().Format(time.RFC3339Nano),
		"schema_version": "1.0",
	}
	if name != "" {
		ev["run_name"] = name
	}
	c.emit(ev)
	return r
}

func (c *Client) StartSingleAgent(ctx context.Context, agentName string, opts ...SessionOption) (*Run, *Session) {
	run := c.StartRun(ctx, agentName)
	session := run.StartSession(ctx, agentName, opts...)
	return run, session
}
