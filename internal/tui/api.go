package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// APIClient talks to the ingest server's read APIs.
type APIClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewAPIClient(baseURL string) *APIClient {
	return &APIClient{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Response types matching the Phase 4 JSON shapes.

type OverviewResponse struct {
	Window        string       `json:"window"`
	TotalRequests int          `json:"total_requests"`
	TotalFailures int          `json:"total_failures"`
	ErrorRate     float64      `json:"error_rate"`
	Sampled       bool         `json:"sampled"`
	TopErrors     []ErrorCount `json:"top_errors"`
	RecentTraces  []TraceEntry `json:"recent_traces"`
}

type ErrorCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type TraceEntry struct {
	TraceID    string    `json:"trace_id"`
	Success    bool      `json:"success"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	EventName  string    `json:"event_name,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

type StoryResponse struct {
	Story   Story        `json:"story"`
	Context TraceContext `json:"context"`
}

type Story struct {
	TraceID      string `json:"trace_id"`
	Chain        []Hop  `json:"chain"`
	Success      bool   `json:"success"`
	FirstFailHop *Hop   `json:"first_fail_hop,omitempty"`
	HopCount     int    `json:"hop_count"`
}

type Hop struct {
	SpanID     string    `json:"span_id"`
	Service    string    `json:"service"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	Success    bool      `json:"success"`
	ErrorCode  string    `json:"error_code,omitempty"`
	IsRoot     bool      `json:"is_root"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
}

type TraceContext struct {
	RequestID    string   `json:"request_id,omitempty"`
	RequestEvent string   `json:"request_event,omitempty"`
	ErrorCodes   []string `json:"error_codes,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	UserTier     string   `json:"user_tier,omitempty"`
	UserRegion   string   `json:"user_region,omitempty"`
	Flow         string   `json:"flow,omitempty"`
	Flags        []string `json:"flags,omitempty"`
}

// Message types for bubbletea.
type overviewMsg OverviewResponse
type storyMsg StoryResponse
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

// FetchOverview fetches the overview endpoint (includes recent traces).
func (c *APIClient) FetchOverview(window string, limit int) tea.Cmd {
	return func() tea.Msg {
		endpoint := fmt.Sprintf("%s/v1/overview?window=%s&limit=%d", c.BaseURL, url.QueryEscape(window), limit)
		resp, err := c.HTTPClient.Get(endpoint)
		if err != nil {
			return errMsg{err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errMsg{fmt.Errorf("overview request failed: %s", resp.Status)}
		}
		var result OverviewResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return errMsg{err}
		}
		return overviewMsg(result)
	}
}

// StartDashboardStream opens an SSE connection to /v1/stream/dashboard and
// returns a channel of tea.Msg values. The underlying goroutine runs until ctx
// is canceled or the server closes the stream.
func (c *APIClient) StartDashboardStream(ctx context.Context) (<-chan tea.Msg, error) {
	endpoint := c.BaseURL + "/v1/stream/dashboard"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	streamClient := &http.Client{} // no timeout: long-lived stream
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream request failed: %s", resp.Status)
	}

	ch := make(chan tea.Msg, 8)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		reader := bufio.NewReader(resp.Body)
		var event string
		var dataBuf strings.Builder
		send := func(msg tea.Msg) bool {
			select {
			case ch <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if event == "overview" && dataBuf.Len() > 0 {
					var ov OverviewResponse
					if jerr := json.Unmarshal([]byte(dataBuf.String()), &ov); jerr == nil {
						if !send(overviewMsg(ov)) {
							return
						}
					}
				}
				event = ""
				dataBuf.Reset()
				continue
			}
			switch {
			case strings.HasPrefix(line, "event: "):
				event = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				dataBuf.WriteString(line[len("data: "):])
			}
		}
	}()
	return ch, nil
}

// WaitForStream returns a tea.Cmd that blocks on the next message from ch.
// When ch closes, it emits an errMsg so the caller can fall back to polling.
func WaitForStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return errMsg{errors.New("dashboard stream closed")}
		}
		return msg
	}
}

// FetchStory fetches the trace story for a given trace ID.
func (c *APIClient) FetchStory(traceID string) tea.Cmd {
	return func() tea.Msg {
		endpoint := fmt.Sprintf("%s/v1/traces/story?trace_id=%s", c.BaseURL, url.QueryEscape(traceID))
		resp, err := c.HTTPClient.Get(endpoint)
		if err != nil {
			return errMsg{err}
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return errMsg{fmt.Errorf("trace %s not found", traceID)}
		}
		if resp.StatusCode != http.StatusOK {
			return errMsg{fmt.Errorf("story request failed: %s", resp.Status)}
		}
		var result StoryResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return errMsg{err}
		}
		return storyMsg(result)
	}
}
