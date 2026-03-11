package agentobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TransportError reports a non-202 response from the server.
type TransportError struct {
	StatusCode int
	Retryable  bool
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("agent-obs server returned %d", e.StatusCode)
}

type transport struct {
	url    string
	apiKey string
	client *http.Client
}

type batchRequest struct {
	Events []any `json:"events"`
}

func newTransport(baseURL, apiKey string) *transport {
	return &transport{
		url:    baseURL + "/v1/agent/events",
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *transport) send(ctx context.Context, events []any) error {
	body, err := json.Marshal(batchRequest{Events: events})
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return &TransportError{
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode >= 500 || resp.StatusCode == 429,
		}
	}
	return nil
}
