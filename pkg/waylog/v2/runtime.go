package waylogv2

import (
	"context"
	"time"
)

const signalPostTimeout = 5 * time.Second

// SafeGo runs fn in a new goroutine, recovering any panic. When runtime hooks
// are enabled, a recovered panic posts a "runtime" signal so it correlates with
// incidents. A bare `go fn()` whose panic goes unrecovered crashes the whole
// process; SafeGo contains it and records the evidence.
func SafeGo(fn func()) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				if s := getState(); s != nil && s.cfg.EnableRuntimeHooks {
					// Post asynchronously (matching FinalizePanic in assemble.go)
					// so a slow/unreachable signal endpoint can't block this
					// goroutine's teardown for up to signalPostTimeout.
					go postPanicSignal(s.cfg, rec)
				}
			}
		}()
		fn()
	}()
}

// postPanicSignal posts a best-effort runtime signal describing a recovered
// panic. It uses a fresh background context with a short timeout, never the
// request context: a client disconnect must not suppress the panic evidence.
func postPanicSignal(cfg Config, recovered any) {
	reason := "panic"
	if recovered != nil {
		reason = "panic: " + sanitizeReason(recovered)
	}
	ctx, cancel := context.WithTimeout(context.Background(), signalPostTimeout)
	defer cancel()
	_ = postSignalWithConfig(ctx, cfg, Signal{
		Type:     "runtime",
		Service:  cfg.Service,
		Env:      cfg.Env,
		Severity: "critical",
		Reason:   reason,
		Message:  reason,
		Source:   "go-sdk",
		Metadata: map[string]any{"subtype": "panic"},
	})
}
