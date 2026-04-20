// e2e-emit emits 3 cascading-failure traces (gateway -> checkout -> payment)
// directly to the ingest /v1/events endpoint and prints the trace_ids, one per
// line, on stdout. Used by scripts/e2e-mark2.sh to drive the Go SDK ingest
// path and exercise the rollup root-cause attribution.
//
// Each trace has 3 spans. The deepest span (payment) carries PMT_502 — the
// true root cause — while checkout and gateway carry propagated codes. With
// the RollupWindow fix, top_errors should report PMT_502=3 and not attribute
// counts to the propagated codes.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
	"github.com/sssmaran/WaylogCLI/pkg/transport"
)

type span struct {
	service, code, msg, reason, path string
	caller, downstream               string
}

func main() {
	ingestURL := "http://localhost:8080"
	if v := os.Getenv("INGEST_URL"); v != "" {
		ingestURL = v
	}

	ht, err := transport.NewHTTPTransport(ingestURL, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "transport init:", err)
		os.Exit(1)
	}
	defer ht.Close(context.Background())

	// Cascade: payment (deepest, root cause) -> checkout -> gateway (root span).
	chain := []span{
		{"payment", "PMT_502", "payment gateway failure",
			"upstream acquirer returned 502",
			"https://runbooks.example.com/payments-502",
			"checkout", ""},
		{"checkout", "CHK_DOWNSTREAM", "checkout propagated failure from payment",
			"downstream payment returned 502", "",
			"gateway", "payment"},
		{"gateway", "GW_DOWNSTREAM", "gateway propagated failure from checkout",
			"downstream checkout returned 502", "",
			"", "checkout"},
	}

	for i := 0; i < 3; i++ {
		traceID := trace.NewTraceID()
		spanIDs := []string{trace.NewSpanID(), trace.NewSpanID(), trace.NewSpanID()}
		now := time.Now().UTC()

		batch := make([]event.WideEvent, len(chain))
		for j, s := range chain {
			parent := ""
			if j+1 < len(spanIDs) {
				parent = spanIDs[j+1]
			}
			batch[j] = mkEvent(now.Add(time.Duration(j)*time.Millisecond),
				traceID, spanIDs[j], parent, s, int64(42+j*15))
		}

		n, err := ht.Send(context.Background(), batch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "send trace %d (%s): sent=%d err=%v\n", i, traceID, n, err)
			os.Exit(1)
		}
		fmt.Println(traceID)
	}
}

func mkEvent(ts time.Time, traceID, spanID, parentSpanID string, s span, latencyMs int64) event.WideEvent {
	ev := event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     s.service + ".error",
		Timestamp:     ts,
		User:          event.UserContext{ID: "e2e-user", Tier: "standard", Region: "us-east-1"},
		Request: event.RequestContext{
			TraceID: traceID, SpanID: spanID, ParentSpanID: parentSpanID,
			Flow: "purchase", HTTPMethod: "POST", FeatureFlags: []string{},
		},
		System: event.SystemContext{
			Service: s.service, Version: "0.1.0", Env: "dev",
			CallerService: s.caller, DownstreamService: s.downstream,
		},
		Outcome: event.OutcomeContext{Success: false, StatusCode: 502, Kind: "http"},
		Error:   &event.ErrorContext{Code: s.code, Message: s.msg, Reason: s.reason, Path: s.path},
		Metrics: event.MetricsContext{LatencyMs: latencyMs},
	}
	if err := ev.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid event for %s: %v\n", s.service, err)
		os.Exit(1)
	}
	return ev
}
