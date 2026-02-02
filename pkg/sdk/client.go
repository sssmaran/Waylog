package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/event"
	"github.com/sssmaran/WaylogCLI/internal/trace"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
		},
	}
}

func (c *Client) Emit(ctx context.Context, ev event.WideEvent) error {
	ensureTraceContext(ctx, &ev)
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("emit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("emit failed: status=%d", resp.StatusCode)
	}

	return nil
}

func ensureTraceContext(ctx context.Context, ev *event.WideEvent) {
	if ev == nil {
		return
	}

	if tc, ok := trace.FromContext(ctx); ok && tc.TraceID != "" {
		if ev.Request.TraceID == "" {
			ev.Request.TraceID = tc.TraceID
		}
		if ev.Request.SpanID == "" {
			parent := ev.Request.ParentSpanID
			if parent == "" {
				parent = tc.SpanID
			}
			child := trace.NewChild(ev.Request.TraceID, parent)
			ev.Request.SpanID = child.SpanID
			ev.Request.ParentSpanID = child.ParentSpanID
		}
		return
	}

	if ev.Request.TraceID == "" {
		tc := trace.NewRoot()
		ev.Request.TraceID = tc.TraceID
		if ev.Request.SpanID == "" {
			ev.Request.SpanID = tc.SpanID
		}
	}
}
