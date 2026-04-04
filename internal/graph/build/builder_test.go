package build

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestBuilder_Build_RequestNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithTraceID("0123456789abcdef0123456789abcdef"),
		testutil.WithService("payment-service"),
		testutil.WithLatency(100),
		testutil.WithStatusCode(200),
	)

	g := builder.Build(ev)

	// Check request node exists
	reqID := core.ID("request", ev.Request.TraceID)
	req, ok := g.Nodes[reqID]
	if !ok {
		t.Fatalf("expected request node %s to exist", reqID)
	}
	if req.Type != core.NodeRequest {
		t.Errorf("expected node type %v, got %v", core.NodeRequest, req.Type)
	}
	if req.Attr["trace_id"] != ev.Request.TraceID {
		t.Errorf("expected trace_id %s, got %v", ev.Request.TraceID, req.Attr["trace_id"])
	}
}

func TestBuilder_Build_ErrorNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithError("ERR_PAYMENT_FAILED", "Payment processing failed"),
	)

	g := builder.Build(ev)

	// Check error node exists
	errID := core.ID("error", ev.Error.Code)
	errNode, ok := g.Nodes[errID]
	if !ok {
		t.Fatalf("expected error node %s to exist", errID)
	}
	if errNode.Type != core.NodeError {
		t.Errorf("expected node type %v, got %v", core.NodeError, errNode.Type)
	}
	if errNode.Attr["code"] != ev.Error.Code {
		t.Errorf("expected code %s, got %v", ev.Error.Code, errNode.Attr["code"])
	}

	// Check request → error edge exists
	reqID := core.ID("request", ev.Request.TraceID)
	hasEdge := false
	for _, e := range g.Edges {
		if e.From == reqID && e.To == errID && e.Type == core.EdgeFailedWith {
			hasEdge = true
			break
		}
	}
	if !hasEdge {
		t.Error("expected request→error edge with EdgeFailedWith")
	}

	req, ok := g.Nodes[reqID]
	if !ok {
		t.Fatalf("expected request node %s to exist", reqID)
	}
	if got := req.Attr["error_code"]; got != ev.Error.Code {
		t.Errorf("request attr error_code = %v, want %s", got, ev.Error.Code)
	}
}

func TestBuilder_Build_SpanErrorEdge_OnlyWhenBothExist(t *testing.T) {
	builder := NewBuilder()

	t.Run("no span, with error", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID(""), // No span
			testutil.WithError("ERR_TEST", "Test error"),
		)

		g := builder.Build(ev)

		// Error node should exist
		errID := core.ID("error", ev.Error.Code)
		if _, ok := g.Nodes[errID]; !ok {
			t.Fatal("expected error node to exist")
		}

		// No span→error edge should exist
		for _, e := range g.Edges {
			if e.Type == core.EdgeFailedWith && e.To == errID {
				fromNode := g.Nodes[e.From]
				if fromNode.Type == core.NodeSpan {
					t.Error("should not have span→error edge when there's no span")
				}
			}
		}

		if got := builder.BuildResult(ev).Span; got != nil {
			t.Fatalf("expected no trace-store span record, got %+v", got)
		}
	})

	t.Run("with span, no error", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			// No error
		)

		result := builder.BuildResult(ev)
		if result.Span == nil {
			t.Fatal("expected trace-store span record to exist")
		}
		if result.Span.SpanID != ev.Request.SpanID {
			t.Fatalf("span record span_id = %q, want %q", result.Span.SpanID, ev.Request.SpanID)
		}
	})

	t.Run("with span and error", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			testutil.WithError("ERR_TEST", "Test error"),
		)

		g := builder.Build(ev)
		errID := core.ID("error", ev.Error.Code)

		if _, ok := g.Nodes[errID]; !ok {
			t.Fatal("expected error node to exist")
		}

		hasRequestErrorEdge := false
		for _, e := range g.Edges {
			if e.To == errID && e.Type == core.EdgeFailedWith {
				fromNode := g.Nodes[e.From]
				if fromNode.Type == core.NodeSpan {
					t.Fatalf("unexpected legacy span→error edge %q -> %q", e.From, e.To)
				}
				if fromNode.Type == core.NodeRequest {
					hasRequestErrorEdge = true
				}
			}
		}
		if !hasRequestErrorEdge {
			t.Error("expected request→error edge with EdgeFailedWith")
		}

		result := builder.BuildResult(ev)
		if result.Span == nil {
			t.Fatal("expected trace-store span record to exist")
		}
		if result.Span.ErrorCode != ev.Error.Code {
			t.Fatalf("span record error_code = %q, want %q", result.Span.ErrorCode, ev.Error.Code)
		}
	})
}

func TestBuilder_Build_NoEmptyEdges(t *testing.T) {
	builder := NewBuilder()

	testCases := []struct {
		name string
		opts []testutil.EventOption
	}{
		{
			name: "success event",
			opts: []testutil.EventOption{testutil.WithStatusCode(200)},
		},
		{
			name: "error event",
			opts: []testutil.EventOption{testutil.WithError("ERR_TEST", "test")},
		},
		{
			name: "error with span",
			opts: []testutil.EventOption{
				testutil.WithSpanID("0123456789abcdef"),
				testutil.WithError("ERR_TEST", "test"),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ev := testutil.MakeEvent(tc.opts...)
			g := builder.Build(ev)

			// Check no edge has empty From or To
			for _, e := range g.Edges {
				if e.From == "" {
					t.Errorf("edge has empty From: %+v", e)
				}
				if e.To == "" {
					t.Errorf("edge has empty To: %+v", e)
				}
			}
		})
	}
}

func TestBuilder_Build_ServiceNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithService("api-gateway"),
	)

	g := builder.Build(ev)

	svcID := core.ID("service", ev.System.Service, ev.System.Env)
	svc, ok := g.Nodes[svcID]
	if !ok {
		t.Fatalf("expected service node %s to exist", svcID)
	}
	if svc.Type != core.NodeService {
		t.Errorf("expected node type %v, got %v", core.NodeService, svc.Type)
	}
	if svc.Attr["name"] != ev.System.Service {
		t.Errorf("expected name %s, got %v", ev.System.Service, svc.Attr["name"])
	}
}

func TestBuilder_Build_UserAttrsOnRequestNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithUser("user-456", "premium", "eu-west-1"),
	)

	g := builder.Build(ev)

	reqID := core.ID("request", ev.Request.TraceID)
	req, ok := g.Nodes[reqID]
	if !ok {
		t.Fatalf("expected request node %s to exist", reqID)
	}
	if req.Attr["user_tier"] != ev.User.Tier {
		t.Errorf("expected user_tier %s, got %v", ev.User.Tier, req.Attr["user_tier"])
	}
	if req.Attr["user_id"] != ev.User.ID {
		t.Errorf("expected user_id %s, got %v", ev.User.ID, req.Attr["user_id"])
	}
	if req.Attr["user_region"] != ev.User.Region {
		t.Errorf("expected user_region %s, got %v", ev.User.Region, req.Attr["user_region"])
	}
	if _, ok := g.Nodes[core.ID("user", ev.User.ID)]; ok {
		t.Fatal("legacy user node should not be present in flattened graph")
	}
}

func TestBuilder_Build_CallerService(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithService("checkout"),
		testutil.WithCallerService("frontend"),
	)

	g := builder.Build(ev)

	callerID := core.ID("service", ev.System.CallerService, ev.System.Env)
	if _, ok := g.Nodes[callerID]; !ok {
		t.Fatalf("expected caller service node %s to exist", callerID)
	}

	// Check calls edge exists
	svcID := core.ID("service", ev.System.Service, ev.System.Env)
	hasEdge := false
	for _, e := range g.Edges {
		if e.From == callerID && e.To == svcID && e.Type == core.EdgeCalls {
			hasEdge = true
			break
		}
	}
	if !hasEdge {
		t.Error("expected caller→service edge with EdgeCalls")
	}
}

func TestBuilder_Build_DownstreamService(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithService("checkout"),
		testutil.WithDownstreamService("payment"),
	)

	g := builder.Build(ev)

	downID := core.ID("service", ev.System.DownstreamService, ev.System.Env)
	if _, ok := g.Nodes[downID]; !ok {
		t.Fatalf("expected downstream service node %s to exist", downID)
	}

	// Check calls edge exists
	svcID := core.ID("service", ev.System.Service, ev.System.Env)
	hasEdge := false
	for _, e := range g.Edges {
		if e.From == svcID && e.To == downID && e.Type == core.EdgeCalls {
			hasEdge = true
			break
		}
	}
	if !hasEdge {
		t.Error("expected service→downstream edge with EdgeCalls")
	}
}

func TestBuilder_Build_FeatureFlagsOnRequestNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithFeatureFlags("dark-mode", "new-checkout"),
	)

	g := builder.Build(ev)

	reqID := core.ID("request", ev.Request.TraceID)
	req := g.Nodes[reqID]
	flags, ok := req.Attr["feature_flags"].([]string)
	if !ok {
		t.Fatalf("feature_flags attr should be []string, got %T", req.Attr["feature_flags"])
	}
	for i, flag := range ev.Request.FeatureFlags {
		if flags[i] != flag {
			t.Fatalf("feature_flags[%d] = %q, want %q", i, flags[i], flag)
		}
		if _, ok := g.Nodes[core.ID("feature_flag", flag)]; ok {
			t.Fatalf("legacy feature_flag node for %q should not be present", flag)
		}
	}
}

func TestBuilder_Build_SpanRecordEnrichedAttrs(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithTraceID("0123456789abcdef0123456789abcdef"),
		testutil.WithSpanID("0123456789abcdef"),
		testutil.WithService("payment-service"),
		testutil.WithLatency(42),
		testutil.WithStatusCode(502),
		testutil.WithCallerService("checkout"),
		testutil.WithDownstreamService("stripe"),
		testutil.WithError("PMT_502", "payment failed"),
	)

	result := builder.BuildResult(ev)
	if result.Span == nil {
		t.Fatal("expected trace-store span record")
	}

	checks := map[string]any{
		"SpanID":            ev.Request.SpanID,
		"ParentSpanID":      ev.Request.ParentSpanID,
		"Service":           "payment-service",
		"EventName":         ev.EventName,
		"StatusCode":        502,
		"Success":           false,
		"LatencyMs":         int64(42),
		"CallerService":     "checkout",
		"DownstreamService": "stripe",
		"ErrorCode":         "PMT_502",
	}
	if result.Span.SpanID != checks["SpanID"] {
		t.Fatalf("SpanID = %q, want %q", result.Span.SpanID, checks["SpanID"])
	}
	if result.Span.ParentSpanID != checks["ParentSpanID"] {
		t.Fatalf("ParentSpanID = %q, want %q", result.Span.ParentSpanID, checks["ParentSpanID"])
	}
	if result.Span.Service != checks["Service"] {
		t.Fatalf("Service = %q, want %q", result.Span.Service, checks["Service"])
	}
	if result.Span.EventName != checks["EventName"] {
		t.Fatalf("EventName = %q, want %q", result.Span.EventName, checks["EventName"])
	}
	if result.Span.StatusCode != checks["StatusCode"] {
		t.Fatalf("StatusCode = %d, want %v", result.Span.StatusCode, checks["StatusCode"])
	}
	if result.Span.Success != checks["Success"] {
		t.Fatalf("Success = %v, want %v", result.Span.Success, checks["Success"])
	}
	if result.Span.LatencyMs != checks["LatencyMs"] {
		t.Fatalf("LatencyMs = %d, want %v", result.Span.LatencyMs, checks["LatencyMs"])
	}
	if result.Span.CallerService != checks["CallerService"] {
		t.Fatalf("CallerService = %q, want %q", result.Span.CallerService, checks["CallerService"])
	}
	if result.Span.DownstreamService != checks["DownstreamService"] {
		t.Fatalf("DownstreamService = %q, want %q", result.Span.DownstreamService, checks["DownstreamService"])
	}
	if result.Span.ErrorCode != checks["ErrorCode"] {
		t.Fatalf("ErrorCode = %q, want %q", result.Span.ErrorCode, checks["ErrorCode"])
	}
	if result.Span.Timestamp.IsZero() {
		t.Fatal("expected Timestamp to be populated")
	}
}

func TestBuilder_Build_RequestIsRoot(t *testing.T) {
	builder := NewBuilder()

	t.Run("root span (no parent)", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			testutil.WithParentSpanID(""),
		)
		g := builder.Build(ev)
		reqID := core.ID("request", ev.Request.TraceID)
		req := g.Nodes[reqID]
		isRoot, ok := req.Attr["is_root"].(bool)
		if !ok || !isRoot {
			t.Errorf("expected is_root=true for root span, got %v", req.Attr["is_root"])
		}
	})

	t.Run("child span (has parent)", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			testutil.WithParentSpanID("fedcba9876543210"),
		)
		g := builder.Build(ev)
		reqID := core.ID("request", ev.Request.TraceID)
		req := g.Nodes[reqID]
		isRoot, ok := req.Attr["is_root"].(bool)
		if !ok || isRoot {
			t.Errorf("expected is_root=false for child span, got %v", req.Attr["is_root"])
		}
	})

	t.Run("no span id", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID(""),
		)
		g := builder.Build(ev)
		reqID := core.ID("request", ev.Request.TraceID)
		req := g.Nodes[reqID]
		isRoot, ok := req.Attr["is_root"].(bool)
		if !ok || isRoot {
			t.Errorf("expected is_root=false when span_id is empty, got %v", req.Attr["is_root"])
		}
	})
}
