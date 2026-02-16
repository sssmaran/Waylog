package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	UserID     string   `json:"user_id,omitempty"`
	UserTier   string   `json:"user_tier,omitempty"`
	UserRegion string   `json:"user_region,omitempty"`
	Flow       string   `json:"flow,omitempty"`
	Flags      []string `json:"flags,omitempty"`
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
