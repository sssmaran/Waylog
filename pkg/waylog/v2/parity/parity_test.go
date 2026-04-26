package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// scenarioRunner carries per-scenario plumbing: a fresh SDK Init, the
// emit buffer the SDK writes to, and the *eventv2.Event captured from
// the relevant Finalize* call.
type scenarioRunner struct {
	t   *testing.T
	buf *bytes.Buffer
}

func newScenario(t *testing.T) *scenarioRunner {
	t.Helper()
	if err := drainSDK(t); err != nil {
		t.Fatalf("pre-test drain: %v", err)
	}
	buf := &bytes.Buffer{}
	cfg := waylogv2.Config{
		Service: "checkout",
		Env:     "test",
		Output:  buf,
	}
	if err := waylogv2.Init(cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return &scenarioRunner{t: t, buf: buf}
}

// drainSDK uses the public API to wait for in-flight requests to finish
// from any prior scenario. It is the public-API-only equivalent of
// resetForTest().
func drainSDK(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return waylogv2.Shutdown(ctx)
}

// emitted parses the most recent JSON line written to the SDK output.
// All scenarios produce exactly one final event.
func (r *scenarioRunner) emitted() []byte {
	r.t.Helper()
	out := r.buf.Bytes()
	if len(out) == 0 {
		r.t.Fatal("no event emitted")
	}
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte{'\n'})
	return lines[len(lines)-1]
}

// assertParity compares the SDK's emitted JSON against the named fixture
// after masking non-deterministic fields. On mismatch it prints both
// blobs side by side so the diff is human-readable in test output.
func (r *scenarioRunner) assertParity(name string) {
	r.t.Helper()
	got := r.emitted()
	want, err := LoadFixture(name)
	if err != nil {
		r.t.Fatalf("load fixture %s: %v", name, err)
	}
	eq, err := Equal(got, want)
	if err != nil {
		r.t.Fatalf("compare: %v", err)
	}
	if !eq {
		gotPretty := jsonPretty(r.t, got)
		wantPretty := jsonPretty(r.t, want)
		gotMasked := jsonPretty(r.t, mustMask(r.t, got))
		wantMasked := jsonPretty(r.t, mustMask(r.t, want))
		r.t.Fatalf("parity mismatch for %s\n--- emitted (raw) ---\n%s\n--- fixture (raw) ---\n%s\n--- emitted (masked) ---\n%s\n--- fixture (masked) ---\n%s",
			name, gotPretty, wantPretty, gotMasked, wantMasked)
	}
}

func mustMask(t *testing.T, raw []byte) []byte {
	t.Helper()
	out, err := MaskNonDeterministic(raw)
	if err != nil {
		t.Fatalf("mask: %v", err)
	}
	return out
}

func jsonPretty(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// --- Scenario 1: ok-simple --------------------------------------------------

func TestParity_OKSimple(t *testing.T) {
	r := newScenario(t)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: strings.Repeat("1", 32),
		SpanID:  strings.Repeat("1", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "POST",
		"route":  "/checkout",
		"status": 200,
	})
	if err := waylogv2.StepVoid(ctx, "db.load_cart", func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("step: %v", err)
	}
	if _, err := waylogv2.Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	r.assertParity("ok-simple.json")
}

// --- Scenario 2: error-payment-cascade --------------------------------------

func TestParity_ErrorPaymentCascade(t *testing.T) {
	r := newScenario(t)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: strings.Repeat("2", 32),
		SpanID:  strings.Repeat("2", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "POST",
		"route":  "/checkout",
		"status": 200, // overwritten by SetHTTPStatus(502) below
	})
	waylogv2.SetField(ctx, "user", map[string]any{"id": "u_123"})

	if err := waylogv2.StepVoid(ctx, "db.load_cart", func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("db step: %v", err)
	}

	wantErr := waylogv2.NewError("PMT_502", waylogv2.WithReason("upstream gateway 5xx"))
	stepErr := waylogv2.StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		waylogv2.From(ctx).Warn("retrying payment", nil)
		waylogv2.RecordOutgoingSpan(ctx, strings.Repeat("3", 16), "payment", "POST /charge")
		waylogv2.Fail(ctx, wantErr)
		return wantErr
	})
	if !errors.Is(stepErr, wantErr) {
		t.Fatalf("expected payment.charge to surface %v, got %v", wantErr, stepErr)
	}

	waylogv2.SetHTTPStatus(ctx, 502)

	if _, err := waylogv2.Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	r.assertParity("error-payment-cascade.json")
}

// --- Scenario 3: error-panic ------------------------------------------------

func TestParity_ErrorPanic(t *testing.T) {
	r := newScenario(t)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: strings.Repeat("6", 32),
		SpanID:  strings.Repeat("7", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "POST",
		"route":  "/checkout",
		"status": 500,
	})
	if _, err := waylogv2.FinalizePanic(ctx); err != nil {
		t.Fatalf("FinalizePanic: %v", err)
	}
	r.assertParity("error-panic.json")
}

// --- Scenario 4: suppressed-healthcheck -------------------------------------

func TestParity_SuppressedHealthcheck(t *testing.T) {
	r := newScenario(t)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: strings.Repeat("5", 32),
		SpanID:  strings.Repeat("6", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "GET",
		"route":  "/healthz",
		"status": 200,
	})
	waylogv2.Suppress(ctx)
	if _, err := waylogv2.Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	r.assertParity("suppressed-healthcheck.json")
}

// --- Scenario 5: aborted-cancel ---------------------------------------------

func TestParity_AbortedCancel(t *testing.T) {
	r := newScenario(t)
	parent, cancel := context.WithCancel(context.Background())
	ctx := waylogv2.Begin(parent, waylogv2.BeginOptions{
		TraceID: strings.Repeat("4", 32),
		SpanID:  strings.Repeat("5", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "POST",
		"route":  "/checkout",
	})
	cancel()
	if _, err := waylogv2.FinalizeAborted(ctx); err != nil {
		t.Fatalf("FinalizeAborted: %v", err)
	}
	r.assertParity("aborted-cancel.json")
}

// --- Scenario 6: timeout-watchdog -------------------------------------------
//
// The fixture encodes a watchdog firing while payment.charge is still
// active: the previous step closed normally, but the watchdog wins the
// seal before payment.charge can return. Reproducing that via public API
// means firing FinalizeTimeout from inside the active step's fn so the
// step is still on the stack at seal time. The SDK's flush-active-steps
// path then snapshots payment.charge into steps[] as the closed
// status-ok entry the fixture expects.

func TestParity_TimeoutWatchdog(t *testing.T) {
	r := newScenario(t)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{
		TraceID: strings.Repeat("3", 32),
		SpanID:  strings.Repeat("4", 16),
	})
	waylogv2.SetField(ctx, "http", map[string]any{
		"method": "POST",
		"route":  "/checkout",
	})

	if err := waylogv2.StepVoid(ctx, "db.load_cart", func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("db.load_cart step: %v", err)
	}

	if err := waylogv2.StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		// Fire the watchdog while still inside the step. Once Finalize
		// seals the request, the surrounding StepVoid wrapper sees the
		// sealed flag and skips its addStep — but the lifecycle path has
		// already snapshotted payment.charge into steps[].
		if _, err := waylogv2.FinalizeTimeout(ctx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("payment.charge step: %v", err)
	}

	r.assertParity("timeout-watchdog.json")
}
