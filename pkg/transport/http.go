package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

const (
	defaultHTTPTimeout = 5 * time.Second
	maxRetries         = 3
)

var retryDelays = [2]time.Duration{100 * time.Millisecond, 300 * time.Millisecond}

// HTTPTransport sends events to an ingest server via HTTP POST.
// Each event in a batch is sent as a separate POST request (one event per request),
// matching the ingest server's single-event decode contract.
type HTTPTransport struct {
	url    string
	apiKey string
	client *http.Client
}

// NewHTTPTransport creates an HTTP transport targeting the given ingest URL.
// It validates the URL upfront: scheme and host must be present, query and fragment
// are rejected to prevent subtle endpoint bugs. If the URL does not already end
// with /v1/events, the path is appended automatically.
func NewHTTPTransport(ingestURL string, timeout time.Duration) (*HTTPTransport, error) {
	ingestURL = strings.TrimRight(ingestURL, "/")

	u, err := url.Parse(ingestURL)
	if err != nil {
		return nil, fmt.Errorf("invalid ingest URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid ingest URL: missing scheme or host: %s", ingestURL)
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("invalid ingest URL: query parameters not allowed: %s", ingestURL)
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("invalid ingest URL: fragment not allowed: %s", ingestURL)
	}

	resolved := ingestURL
	if !strings.HasSuffix(resolved, "/v1/events") {
		resolved += "/v1/events"
	}

	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}

	return &HTTPTransport{
		url:    resolved,
		apiKey: os.Getenv("WAYLOG_API_KEY"),
		client: &http.Client{Timeout: timeout},
	}, nil
}

// Send posts each event individually. Returns (sentCount, err) where sentCount
// is the number of events successfully delivered before any permanent failure.
func (t *HTTPTransport) Send(ctx context.Context, batch []event.WideEvent) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	sent := 0
	for i := range batch {
		if err := ctx.Err(); err != nil {
			return sent, err
		}
		if err := t.sendOne(ctx, &batch[i]); err != nil {
			return sent, fmt.Errorf("event %d/%d: %w", i+1, len(batch), err)
		}
		sent++
	}
	return sent, nil
}

func (t *HTTPTransport) sendOne(ctx context.Context, ev *event.WideEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelays[attempt-1]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if t.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+t.apiKey)
		}

		resp, err := t.client.Do(req)
		if err != nil {
			if isTransientNetErr(err) {
				lastErr = err
				continue
			}
			return err // permanent error (TLS, DNS, etc.) — no retry
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue // 503, retry
		}
		// Non-retriable error
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("retries exhausted: %w", lastErr)
}

// Close checks for context cancellation, consistent with other transports.
func (t *HTTPTransport) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// isTransientNetErr returns true only for syscall-level connection errors
// (ECONNREFUSED, ECONNRESET, ECONNABORTED) and per-request timeouts.
// DNS resolution failures, TLS errors, and other permanent issues return false.
func isTransientNetErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	// DNS and TLS errors are permanent — don't retry.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	var tlsCertErr *tls.CertificateVerificationError
	if errors.As(err, &tlsCertErr) {
		return false
	}
	var tlsRecordErr *tls.RecordHeaderError
	if errors.As(err, &tlsRecordErr) {
		return false
	}

	// Per-request timeout (net.Error.Timeout) is transient.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Only retry specific syscall errors — connection refused/reset/aborted.
	// This avoids retrying misconfigured endpoints that produce other OpError types.
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}

	return false
}
