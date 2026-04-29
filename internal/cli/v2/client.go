package cliv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "http://localhost:8080"

type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

type APIError struct {
	Status  int
	Code    string
	Message string
	Detail  string
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

type TransportError struct {
	Err error
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("INGEST_ADDR")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("WAYLOG_READ_KEY")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = parseEnvDuration("WAYLOG_CLI_TIMEOUT", 5*time.Second)
	}
	return &Client{
		base:   NormalizeBaseURL(cfg.BaseURL),
		apiKey: cfg.APIKey,
		http:   &http.Client{Timeout: cfg.Timeout},
	}
}

func NormalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultBaseURL
	}
	if strings.HasPrefix(raw, ":") {
		raw = "localhost" + raw
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func (c *Client) Capabilities(ctx context.Context) (CapabilitiesResponse, error) {
	var out CapabilitiesResponse
	err := c.do(ctx, "/v1/capabilities", nil, &out)
	return out, err
}

func (c *Client) Errors(ctx context.Context, p ErrorsParams) (ErrorsResponse, error) {
	q := url.Values{}
	addQuery(q, "window", p.Window)
	addQuery(q, "service", p.Service)
	addQuery(q, "cursor", p.Cursor)
	addLimit(q, p.Limit)
	var out ErrorsResponse
	err := c.do(ctx, "/v1/errors", q, &out)
	return out, err
}

func (c *Client) Trace(ctx context.Context, traceID string) (TraceGetResponse, error) {
	var out TraceGetResponse
	err := c.do(ctx, "/v1/traces/"+url.PathEscape(traceID), nil, &out)
	return out, err
}

func (c *Client) Story(ctx context.Context, q StoryQuery) (StoryResponse, error) {
	values := url.Values{}
	addQuery(values, "event_id", q.EventID)
	addQuery(values, "trace_id", q.TraceID)
	var out StoryResponse
	err := c.do(ctx, "/v1/traces/story", values, &out)
	return out, err
}

func (c *Client) Blast(ctx context.Context, p BlastParams) (BlastRadiusResponse, error) {
	q := url.Values{}
	addQuery(q, "service", p.Service)
	addQuery(q, "step", p.Step)
	addQuery(q, "error_code", p.ErrorCode)
	addQuery(q, "error_family", p.ErrorFamily)
	addQuery(q, "window", p.Window)
	var out BlastRadiusResponse
	err := c.do(ctx, "/v1/blast_radius", q, &out)
	return out, err
}

func (c *Client) Search(ctx context.Context, p SearchParams) (EventSearchResponse, error) {
	q := url.Values{}
	addQuery(q, "error_code", p.ErrorCode)
	addQuery(q, "trace_id", p.TraceID)
	addQuery(q, "service", p.Service)
	addQuery(q, "status", p.Status)
	addQuery(q, "window", p.Window)
	addQuery(q, "cursor", p.Cursor)
	addLimit(q, p.Limit)
	var out EventSearchResponse
	err := c.do(ctx, "/v1/events/search", q, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, path string, q url.Values, out any) error {
	u, err := url.Parse(c.base + path)
	if err != nil {
		return &TransportError{Err: err}
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return &TransportError{Err: err}
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &TransportError{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &TransportError{Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, body)
	}
	if out == nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &TransportError{Err: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}

func decodeAPIError(status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		return &APIError{Status: status, Code: env.Error.Code, Message: env.Error.Message, Detail: env.Error.Detail}
	}
	code := "unavailable"
	if status == http.StatusUnauthorized {
		code = "unauthorized"
	} else if status >= 400 && status < 500 {
		code = "bad_request"
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Code: code, Message: msg}
}

func exitCodeForError(err error) int {
	var transport *TransportError
	if errors.As(err, &transport) {
		return 2
	}
	var api *APIError
	if errors.As(err, &api) {
		if api.Status >= 500 {
			return 4
		}
		return 3
	}
	return 2
}

func addQuery(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

func addLimit(q url.Values, limit int) {
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
}

func parseEnvDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
