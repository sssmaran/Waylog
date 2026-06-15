// Package firstrun runs the self-contained Crux first-run demo: launch the
// ingest server, drive a real failure burst through the SDK, and print the
// deterministic incident report.
package firstrun

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// burstHTTPClient is a package-level client with a fixed timeout so postJSON
// never hangs indefinitely (mirrors the SDK signal client's 5 s policy).
var burstHTTPClient = &http.Client{Timeout: 5 * time.Second}

// BurstConfig controls how many failing checkout events RunBurst emits.
type BurstConfig struct {
	IngestURL string
	WriteKey  string
	Requests  int // failing checkout requests to emit; default 25
}

// BurstResult reports what RunBurst produced.
type BurstResult struct {
	FailingEvents int
}

const (
	burstService   = "checkout"
	burstStep      = "payment.charge"
	burstCode      = "PMT_502"
	burstEnv       = "demo"
	burstTestCount = 10 // number of failing requests used in unit tests
)

// RunBurst emits Requests failing checkout events through the real SDK so they
// traverse the ingest → v2 reader → incident engine pipeline, then posts one
// alert and one runtime signal to give the incident engine corroborating
// evidence.
func RunBurst(cfg BurstConfig) (BurstResult, error) {
	if cfg.Requests <= 0 {
		cfg.Requests = 25
	}

	if err := waylog.Init(waylog.Config{
		Service:   burstService,
		Env:       burstEnv,
		IngestURL: cfg.IngestURL,
		APIKey:    cfg.WriteKey,
	}); err != nil {
		return BurstResult{}, fmt.Errorf("sdk init: %w", err)
	}

	for i := 0; i < cfg.Requests; i++ {
		ctx := waylog.Begin(context.Background(), waylog.BeginOptions{})
		_ = waylog.StepVoid(ctx, burstStep, func(ctx context.Context) error {
			return waylog.NewError(burstCode, waylog.WithReason("upstream payment gateway 5xx"))
		})
		if _, err := waylog.Finalize(ctx); err != nil {
			return BurstResult{}, fmt.Errorf("finalize event %d: %w", i, err)
		}
	}

	// Flush all buffered events to the ingest server before posting signals.
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := waylog.Shutdown(flushCtx); err != nil {
		return BurstResult{}, fmt.Errorf("sdk shutdown: %w", err)
	}

	if err := postAlert(cfg.IngestURL, cfg.WriteKey); err != nil {
		return BurstResult{}, err
	}
	if err := postRuntimeSignal(cfg.IngestURL, cfg.WriteKey); err != nil {
		return BurstResult{}, err
	}
	return BurstResult{FailingEvents: cfg.Requests}, nil
}

func postJSON(url, key, body string) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := burstHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

func postAlert(ingestURL, key string) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"source":"crux","alert_id":"alert_firstrun_pmt_502","service":"checkout","env":"demo","severity":"critical","reason":"PMT_502 spike","message":"first-run demo alert for checkout payment failures","error_code":"PMT_502","timestamp":%q}`, ts)
	return postJSON(ingestURL+"/v1/alerts", key, body)
}

func postRuntimeSignal(ingestURL, key string) error {
	ts := time.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"type":"runtime","source":"k8s-demo","service":"checkout","env":"demo","severity":"critical","reason":"OOMKilled","message":"Container checkout killed by OOM (limit: 256Mi, usage: 312Mi).","resource":{"service":"checkout","container":"checkout"},"metadata":{"subtype":"oom_killed","pod":"checkout-7f8b9c-x2k","container":"checkout"},"timestamp":%q}`, ts)
	return postJSON(ingestURL+"/v1/signals", key, body)
}
