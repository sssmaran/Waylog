package transporthttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type Config struct {
	IngestURL string
	APIKey    string
	Timeout   time.Duration

	BatchMode    bool
	MaxBatch     int
	MaxBatchSize int
	BatchAgeMs   int
	OkBudgetPct  int
	InFlightCap  int64
}

type Client struct {
	cfg    Config
	url    string
	http   *http.Client
	queue  *queue
	closed sync.Once
}

func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 256
	}
	if cfg.MaxBatchSize <= 0 {
		cfg.MaxBatchSize = 1 << 20
	}
	if cfg.BatchAgeMs <= 0 {
		cfg.BatchAgeMs = 50
	}
	if cfg.OkBudgetPct <= 0 {
		cfg.OkBudgetPct = 70
	}
	if cfg.InFlightCap <= 0 {
		cfg.InFlightCap = 10 << 20
	}

	c := &Client{
		cfg:  cfg,
		url:  normalizeIngestURL(cfg.IngestURL),
		http: &http.Client{Timeout: cfg.Timeout},
	}
	if cfg.BatchMode {
		c.queue = newQueue(cfg, c.flushBatch)
		go c.queue.run()
	}
	return c
}

func normalizeIngestURL(raw string) string {
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if strings.HasSuffix(raw, "/v1/events") {
		return raw
	}
	return raw + "/v1/events"
}

func (c *Client) Submit(ev *eventv2.Event) bool {
	if ev == nil || c.url == "" {
		return false
	}
	if c.queue != nil {
		return c.queue.enqueue(ev)
	}
	return c.submitSingle(ev)
}

func (c *Client) Shutdown(timeout time.Duration) {
	c.closed.Do(func() {
		if c.queue != nil {
			c.queue.shutdown(timeout)
		}
	})
}

func (c *Client) submitSingle(ev *eventv2.Event) bool {
	if ev == nil || c.url == "" {
		return false
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
