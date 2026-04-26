package transporthttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type deliveryResult struct {
	success    bool
	retryable  bool
	retryAfter time.Duration
}

func (c *Client) flushBatch(batch []*eventv2.Event) deliveryResult {
	if c.url == "" || len(batch) == 0 {
		return deliveryResult{success: true}
	}

	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, ev := range batch {
		if err := enc.Encode(ev); err != nil {
			c.recordDrop(1)
		}
	}
	if body.Len() == 0 {
		return deliveryResult{success: true}
	}

	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body.Bytes()))
	if err != nil {
		c.recordDrop(len(batch))
		return deliveryResult{}
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.recordFailure(len(batch))
		return deliveryResult{retryable: true}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return deliveryResult{success: true}
	case isRetryableStatus(resp.StatusCode):
		c.recordFailure(len(batch))
		return deliveryResult{
			retryable:  true,
			retryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	default:
		c.recordDrop(len(batch))
		return deliveryResult{}
	}
}

func retryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
