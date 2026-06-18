package waylogv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxSignalReasonLen  = 512
	maxSignalMessageLen = 4096
)

// signalClient is shared across signal posts. Signals are rare, so a small
// pooled client with a short timeout is plenty.
var signalClient = &http.Client{Timeout: 5 * time.Second}

// Signal is a production-context signal posted to /v1/signals. The SDK emits
// "runtime" signals (recovered panics, uncaught errors) so they correlate with
// incidents during triage. Type, Service, Env, Source, Reason and Timestamp are
// required by the server; PostSignal fills Service/Env/Timestamp from config
// when unset.
type Signal struct {
	Type      string         `json:"type"`
	Service   string         `json:"service"`
	Env       string         `json:"env"`
	Severity  string         `json:"severity,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Message   string         `json:"message,omitempty"`
	Source    string         `json:"source,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// PostSignal sends a production signal to the configured ingest server. It is a
// no-op when the SDK is not initialized or when neither SignalURL nor IngestURL
// is configured. Service, Env and Timestamp default to the SDK config / now when
// unset. Best-effort and synchronous: callers should not block on the result.
func PostSignal(ctx context.Context, sig Signal) error {
	s := getState()
	if s == nil {
		return nil
	}
	return postSignalWithConfig(ctx, s.cfg, sig)
}

func postSignalWithConfig(ctx context.Context, cfg Config, sig Signal) error {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := signalURL(cfg)
	if endpoint == "" {
		return nil
	}
	if sig.Service == "" {
		sig.Service = cfg.Service
	}
	if sig.Env == "" {
		sig.Env = cfg.Env
	}
	if sig.Timestamp.IsZero() {
		sig.Timestamp = time.Now().UTC()
	}
	// Bound reason and message for every signal (parity with the TS SDK, which
	// truncates both in its signal transport) so no SDK caller can ship an
	// unbounded payload. This is the single place the size cap is applied.
	sig.Reason = boundString(sig.Reason, maxSignalReasonLen)
	sig.Message = boundString(sig.Message, maxSignalMessageLen)

	body, err := json.Marshal(sig)
	if err != nil {
		return fmt.Errorf("waylog: marshal signal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("waylog: build signal request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := signalClient.Do(req)
	if err != nil {
		return fmt.Errorf("waylog: post signal: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("waylog: signal endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// signalURL resolves the /v1/signals endpoint. SignalURL wins when set;
// otherwise it derives from IngestURL, replacing a trailing /v1/events path with
// /v1/signals and preserving any query parameters.
func signalURL(cfg Config) string {
	if cfg.SignalURL != "" {
		return cfg.SignalURL
	}
	if cfg.IngestURL == "" {
		return ""
	}
	u, err := url.Parse(cfg.IngestURL)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/v1/events") {
		path = strings.TrimSuffix(path, "/v1/events")
	}
	u.Path = path + "/v1/signals"
	return u.String()
}

// sanitizeReason stringifies a recovered panic value and trims whitespace. The
// size cap is applied centrally in postSignalWithConfig, so this does not bound
// length itself. Full field redaction is a later concern.
func sanitizeReason(v any) string {
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

// boundString caps s to at most n bytes without splitting a multibyte UTF-8
// rune: if the cut lands mid-sequence it steps back off the trailing
// continuation bytes, so a truncated reason/message stays valid UTF-8 in the
// signal JSON. Mirrors the TS SDK's transport truncation.
func boundString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
