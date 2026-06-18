package microdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const demoSignalTimeout = 2 * time.Second

type SignalResult struct {
	Type     string `json:"type"`
	Service  string `json:"service"`
	Reason   string `json:"reason"`
	Accepted bool   `json:"accepted"`
	Status   int    `json:"status,omitempty"`
	SignalID string `json:"signal_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

type SignalPoster interface {
	PostDemoSignals(ctx context.Context) []SignalResult
}

type DemoSignalPoster struct {
	ingestURL string
	apiKey    string
	client    *http.Client
	now       func() time.Time
}

func NewDemoSignalPoster(ingestURL, apiKey string) *DemoSignalPoster {
	return &DemoSignalPoster{
		ingestURL: strings.TrimRight(strings.TrimSpace(ingestURL), "/"),
		apiKey:    strings.TrimSpace(apiKey),
		client:    &http.Client{Timeout: demoSignalTimeout},
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (p *DemoSignalPoster) PostDemoSignals(ctx context.Context) []SignalResult {
	specs := []demoSignalSpec{
		{
			Type:     "deploy",
			Service:  "checkout",
			Severity: "info",
			Reason:   "demo_checkout_rollout",
			Message:  "Demo checkout rollout before the payment dependency incident.",
			Resource: map[string]any{"service": "checkout"},
			Metadata: map[string]any{"version": "demo-v2.1", "demo": "traffic_burst"},
		},
		{
			Type:     "dependency",
			Service:  "payment",
			Severity: "critical",
			Reason:   "payment_gateway_5xx",
			Message:  "Demo payment provider is returning intermittent 5xx responses.",
			Resource: map[string]any{"service": "payment", "endpoint": "POST /charge"},
			Metadata: map[string]any{"error_code": "PMT_502", "downstream": "payment", "demo": "traffic_burst"},
		},
		{
			// Infra runtime evidence. Targets checkout — the service the burst
			// incident opens on (checkout:payment.charge:PMT_502) — so it
			// correlates onto the same incident that already carries the alert,
			// dependency, propagation and blast evidence (Critical Design
			// Decision 3: runtime signals match by inc.Service). Source k8s-demo
			// marks it as infrastructure-runtime in the dashboard.
			Type:     "runtime",
			Service:  "checkout",
			Severity: "critical",
			Reason:   "OOMKilled",
			Message:  "Container checkout killed by OOM (limit: 256Mi, usage: 312Mi).",
			Source:   "k8s-demo",
			Resource: map[string]any{"service": "checkout", "container": "checkout"},
			Metadata: map[string]any{"subtype": "oom_killed", "pod": "checkout-7f8b9c-x2k", "container": "checkout", "demo": "traffic_burst"},
		},
	}

	results := make([]SignalResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, p.postSignal(ctx, spec))
	}
	return results
}

func (p *DemoSignalPoster) postSignal(ctx context.Context, spec demoSignalSpec) SignalResult {
	result := SignalResult{Type: spec.Type, Service: spec.Service, Reason: spec.Reason}
	if p == nil || p.ingestURL == "" {
		result.Error = "INGEST_URL is not configured"
		return result
	}

	body, err := json.Marshal(spec.body(p.now()))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	reqCtx, cancel := context.WithTimeout(ctx, demoSignalTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.ingestURL+"/v1/signals", bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("X-API-Key", p.apiKey)
	}

	client := p.client
	if client == nil {
		client = &http.Client{Timeout: demoSignalTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusCreated {
		result.Error = fmt.Sprintf("signal POST returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		return result
	}
	var accepted struct {
		Signal struct {
			SignalID string `json:"signal_id"`
		} `json:"signal"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		result.Error = "accepted signal response was not valid JSON: " + err.Error()
		return result
	}
	result.Accepted = true
	result.SignalID = accepted.Signal.SignalID
	return result
}

type demoSignalSpec struct {
	Type     string
	Service  string
	Severity string
	Reason   string
	Message  string
	Source   string // signal source; defaults to "waylog-demo" when empty
	Resource map[string]any
	Metadata map[string]any
}

func (s demoSignalSpec) body(ts time.Time) map[string]any {
	source := s.Source
	if source == "" {
		source = "waylog-demo"
	}
	return map[string]any{
		"type":      s.Type,
		"source":    source,
		"service":   s.Service,
		"env":       "demo",
		"severity":  s.Severity,
		"reason":    s.Reason,
		"message":   s.Message,
		"resource":  s.Resource,
		"metadata":  s.Metadata,
		"timestamp": ts.UTC(),
	}
}
