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
	})

	t.Run("with span, no error", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			// No error
		)

		g := builder.Build(ev)

		// Span should exist
		spanID := core.ID("span", ev.Request.TraceID, ev.Request.SpanID)
		if _, ok := g.Nodes[spanID]; !ok {
			t.Fatal("expected span node to exist")
		}

		// No EdgeFailedWith edges from span
		for _, e := range g.Edges {
			if e.From == spanID && e.Type == core.EdgeFailedWith {
				t.Errorf("should not have span→error edge when there's no error, but found edge to %s", e.To)
			}
		}
	})

	t.Run("with span and error", func(t *testing.T) {
		ev := testutil.MakeEvent(
			testutil.WithSpanID("0123456789abcdef"),
			testutil.WithError("ERR_TEST", "Test error"),
		)

		g := builder.Build(ev)

		spanID := core.ID("span", ev.Request.TraceID, ev.Request.SpanID)
		errID := core.ID("error", ev.Error.Code)

		// Both should exist
		if _, ok := g.Nodes[spanID]; !ok {
			t.Fatal("expected span node to exist")
		}
		if _, ok := g.Nodes[errID]; !ok {
			t.Fatal("expected error node to exist")
		}

		// span→error edge should exist
		hasEdge := false
		for _, e := range g.Edges {
			if e.From == spanID && e.To == errID && e.Type == core.EdgeFailedWith {
				hasEdge = true
				break
			}
		}
		if !hasEdge {
			t.Error("expected span→error edge with EdgeFailedWith")
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

func TestBuilder_Build_UserNode(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithUser("user-456", "premium", "eu-west-1"),
	)

	g := builder.Build(ev)

	userID := core.ID("user", ev.User.ID)
	user, ok := g.Nodes[userID]
	if !ok {
		t.Fatalf("expected user node %s to exist", userID)
	}
	if user.Type != core.NodeUser {
		t.Errorf("expected node type %v, got %v", core.NodeUser, user.Type)
	}
	if user.Attr["tier"] != ev.User.Tier {
		t.Errorf("expected tier %s, got %v", ev.User.Tier, user.Attr["tier"])
	}
}

func TestBuilder_Build_CallerService(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithService("checkout"),
		testutil.WithCallerService("frontend"),
	)

	g := builder.Build(ev)

	callerID := core.ID("service", ev.System.CallerService)
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

	downID := core.ID("service", ev.System.DownstreamService)
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

func TestBuilder_Build_FeatureFlags(t *testing.T) {
	builder := NewBuilder()
	ev := testutil.MakeEvent(
		testutil.WithFeatureFlags("dark-mode", "new-checkout"),
	)

	g := builder.Build(ev)

	reqID := core.ID("request", ev.Request.TraceID)

	for _, flag := range ev.Request.FeatureFlags {
		flagID := core.ID("feature_flag", flag)
		flagNode, ok := g.Nodes[flagID]
		if !ok {
			t.Errorf("expected flag node %s to exist", flagID)
			continue
		}
		if flagNode.Type != core.NodeFlag {
			t.Errorf("expected node type %v, got %v", core.NodeFlag, flagNode.Type)
		}

		// Check edge
		hasEdge := false
		for _, e := range g.Edges {
			if e.From == reqID && e.To == flagID && e.Type == core.EdgeUsedFlag {
				hasEdge = true
				break
			}
		}
		if !hasEdge {
			t.Errorf("expected request→flag edge for %s", flag)
		}
	}
}
