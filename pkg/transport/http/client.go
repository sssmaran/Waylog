package transporthttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	defaultMaxRetries = 5
	defaultBackoffMin = 100 * time.Millisecond
	defaultBackoffMax = 5 * time.Second
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
	MaxRetries   int
}

type Client struct {
	cfg    Config
	url    string
	http   *http.Client
	queue  *queue
	closed sync.Once

	dropped  atomic.Int64
	failures atomic.Int64
}

func New(cfg Config) (*Client, error) {
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
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultMaxRetries
	}

	ingestURL, ok := NormalizeIngestURL(cfg.IngestURL)
	if !ok {
		return nil, &InvalidIngestURLError{URL: cfg.IngestURL}
	}

	c := &Client{
		cfg:  cfg,
		url:  ingestURL,
		http: &http.Client{Timeout: cfg.Timeout},
	}
	if cfg.BatchMode {
		c.queue = newQueue(cfg, c.flushBatch, c.recordDrop)
		go c.queue.run()
	}
	return c, nil
}

type InvalidIngestURLError struct {
	URL string
}

func (e *InvalidIngestURLError) Error() string {
	return "waylog transport: invalid ingest URL " + strconv.Quote(e.URL)
}

func NormalizeIngestURL(raw string) (string, bool) {
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if strings.HasSuffix(u.Path, "/v1/events") {
		return raw, true
	}
	return raw + "/v1/events", true
}

func (c *Client) Submit(ev *eventv2.Event) bool {
	if ev == nil || c.url == "" {
		return false
	}
	if c.queue != nil {
		if c.queue.enqueue(ev) {
			return true
		}
		c.recordDrop(1)
		return false
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
		c.recordDrop(1)
		return false
	}
	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		c.recordDrop(1)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.recordFailure(1)
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true
	}
	if isRetryableStatus(resp.StatusCode) {
		c.recordFailure(1)
	} else {
		c.recordDrop(1)
	}
	return false
}

func (c *Client) Dropped() int64 {
	if c == nil {
		return 0
	}
	return c.dropped.Load()
}

func (c *Client) Failures() int64 {
	if c == nil {
		return 0
	}
	return c.failures.Load()
}

func (c *Client) recordDrop(n int) {
	if n > 0 {
		c.dropped.Add(int64(n))
	}
}

func (c *Client) recordFailure(n int) {
	if n > 0 {
		c.failures.Add(int64(n))
	}
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}
