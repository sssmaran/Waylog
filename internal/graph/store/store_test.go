package store

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestStore_Merge_RequestDeterministicMerge(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"

	// Child event arrives first (non-root)
	childEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithParentSpanID("pppppppppppppppp"),
		testutil.WithService("payment-demo"),
		testutil.WithStatusCode(502),
		testutil.WithLatency(12),
		testutil.WithError("PMT_502", "payment failed"),
		testutil.WithEventName("payment-demo.error"),
	)
	s.Merge(b.Build(childEv))

	// Root event arrives second
	rootEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID("pppppppppppppppp"),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(rootEv))

	// Verify request node
	snap := s.Snapshot()
	reqID := core.ID("request", traceID)
	req, ok := snap.Nodes[reqID]
	if !ok {
		t.Fatalf("request node %s not found", reqID)
	}

	// Root's values should win for status_code, latency_ms, event_name
	if got := req.Attr["status_code"]; got != 200 {
		t.Errorf("status_code = %v, want 200 (from root)", got)
	}
	if got := req.Attr["latency_ms"]; got != int64(45) {
		t.Errorf("latency_ms = %v, want 45 (from root)", got)
	}
	if got := req.Attr["event_name"]; got != "api-gateway.request" {
		t.Errorf("event_name = %v, want api-gateway.request (from root)", got)
	}

	// success should be AND: child was false, so overall false
	if got := req.Attr["success"]; got != false {
		t.Errorf("success = %v, want false (AND of child failure)", got)
	}

	// is_root should be true (root event set it)
	if got := req.Attr["is_root"]; got != true {
		t.Errorf("is_root = %v, want true", got)
	}

	codes, ok := req.Attr["error_codes"].([]string)
	if !ok {
		t.Fatalf("error_codes should be []string, got %T (%v)", req.Attr["error_codes"], req.Attr["error_codes"])
	}
	if len(codes) != 1 || codes[0] != "PMT_502" {
		t.Errorf("error_codes = %v, want [PMT_502]", codes)
	}
}

func TestStore_Merge_SpanStubEnrichment(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	parentSpanID := "1111111111111111"
	childSpanID := "2222222222222222"

	// Child event arrives first — creates a stub for parent span
	childEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(childSpanID),
		testutil.WithParentSpanID(parentSpanID),
		testutil.WithService("checkout-demo"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(32),
	)
	s.Merge(b.Build(childEv))

	// Verify parent span is a stub (missing enriched fields)
	snap := s.Snapshot()
	parentNodeID := core.ID("span", traceID, parentSpanID)
	parentNode, ok := snap.Nodes[parentNodeID]
	if !ok {
		t.Fatalf("parent span stub %s not found", parentNodeID)
	}
	if _, has := parentNode.Attr["status_code"]; has {
		t.Error("parent stub should not have status_code yet")
	}

	// Now parent's own event arrives — should enrich the stub
	parentEv := testutil.MakeEvent(
		testutil.WithTraceID(traceID),
		testutil.WithSpanID(parentSpanID),
		testutil.WithParentSpanID(""),
		testutil.WithService("api-gateway"),
		testutil.WithStatusCode(200),
		testutil.WithLatency(45),
		testutil.WithEventName("api-gateway.request"),
	)
	s.Merge(b.Build(parentEv))

	snap = s.Snapshot()
	parentNode = snap.Nodes[parentNodeID]

	// Verify enriched attrs
	if got := parentNode.Attr["service"]; got != "api-gateway" {
		t.Errorf("service = %v, want api-gateway", got)
	}
	if got := parentNode.Attr["status_code"]; got != 200 {
		t.Errorf("status_code = %v, want 200", got)
	}
	if got := parentNode.Attr["latency_ms"]; got != int64(45) {
		t.Errorf("latency_ms = %v, want 45", got)
	}
	if got := parentNode.Attr["event_name"]; got != "api-gateway.request" {
		t.Errorf("event_name = %v, want api-gateway.request", got)
	}
}
