package analysis

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestBuildTopology_BasicGraph(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-10 * time.Minute)
	end := now.Add(time.Minute)

	builder := build.NewBuilder()
	st := graphstore.NewStore()

	// Event 1: frontend calls api-gateway (success)
	ev1 := testutil.MakeEvent(
		testutil.WithService("api-gateway"),
		testutil.WithCallerService("frontend"),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithTimestamp(now),
	)
	st.Merge(builder.Build(ev1))

	// Event 2: api-gateway calls checkout (success)
	ev2 := testutil.MakeEvent(
		testutil.WithService("checkout"),
		testutil.WithCallerService("api-gateway"),
		testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithTraceID("abcdef01234567890abcdef012345678"),
		testutil.WithTimestamp(now),
	)
	st.Merge(builder.Build(ev2))

	// Event 3: api-gateway calls checkout (error)
	ev3 := testutil.MakeEvent(
		testutil.WithService("checkout"),
		testutil.WithCallerService("api-gateway"),
		testutil.WithSpanID("cccccccccccccccc"),
		testutil.WithTraceID("abcdef01234567890abcdef012345679"),
		testutil.WithError("CHK_500", "internal error"),
		testutil.WithTimestamp(now),
	)
	st.Merge(builder.Build(ev3))

	result := BuildTopology(st.Snapshot(), start, end)

	// Should have 3 service nodes: frontend, api-gateway, checkout
	if len(result.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(result.Nodes))
	}

	nodeByID := map[string]TopologyNode{}
	for _, n := range result.Nodes {
		nodeByID[n.ID] = n
	}

	// api-gateway: 1 invocation, 0 errors
	gw, ok := nodeByID["api-gateway"]
	if !ok {
		t.Fatal("missing api-gateway node")
	}
	if gw.Invocations != 1 {
		t.Errorf("api-gateway invocations: got %d, want 1", gw.Invocations)
	}
	if gw.Errors != 0 {
		t.Errorf("api-gateway errors: got %d, want 0", gw.Errors)
	}
	if gw.Status != "healthy" {
		t.Errorf("api-gateway status: got %q, want %q", gw.Status, "healthy")
	}

	// checkout: 2 invocations, 1 error -> error_rate 0.5 -> "failing"
	ck, ok := nodeByID["checkout"]
	if !ok {
		t.Fatal("missing checkout node")
	}
	if ck.Invocations != 2 {
		t.Errorf("checkout invocations: got %d, want 2", ck.Invocations)
	}
	if ck.Errors != 1 {
		t.Errorf("checkout errors: got %d, want 1", ck.Errors)
	}
	if ck.Status != "failing" {
		t.Errorf("checkout status: got %q, want %q", ck.Status, "failing")
	}

	// Should have 2 edges: frontend->api-gateway, api-gateway->checkout
	if len(result.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(result.Edges))
	}

	edgeFound := map[string]bool{}
	for _, e := range result.Edges {
		edgeFound[e.Source+"->"+e.Target] = true
	}
	if !edgeFound["frontend->api-gateway"] {
		t.Error("missing edge frontend->api-gateway")
	}
	if !edgeFound["api-gateway->checkout"] {
		t.Error("missing edge api-gateway->checkout")
	}

	// Check edge stats for api-gateway->checkout
	for _, e := range result.Edges {
		if e.Source == "api-gateway" && e.Target == "checkout" {
			if e.Requests != 2 {
				t.Errorf("edge requests: got %d, want 2", e.Requests)
			}
			if e.Failures != 1 {
				t.Errorf("edge failures: got %d, want 1", e.Failures)
			}
		}
	}
}

func TestBuildTopology_EmptyGraph(t *testing.T) {
	st := graphstore.NewStore()
	now := time.Now().UTC()
	result := BuildTopology(st.Snapshot(), now.Add(-time.Hour), now)

	if result.Nodes == nil {
		t.Error("Nodes should be non-nil empty slice")
	}
	if result.Edges == nil {
		t.Error("Edges should be non-nil empty slice")
	}
	if len(result.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(result.Nodes))
	}
	if len(result.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(result.Edges))
	}
}

func TestBuildTopology_WindowFiltering(t *testing.T) {
	now := time.Now().UTC()
	builder := build.NewBuilder()
	st := graphstore.NewStore()

	// Event inside window
	ev1 := testutil.MakeEvent(
		testutil.WithService("svc-a"),
		testutil.WithCallerService("svc-b"),
		testutil.WithSpanID("aaaaaaaaaaaaaaaa"),
		testutil.WithTimestamp(now),
	)
	st.Merge(builder.Build(ev1))

	// Event outside window (2 hours ago)
	ev2 := testutil.MakeEvent(
		testutil.WithService("svc-old"),
		testutil.WithCallerService("svc-ancient"),
		testutil.WithSpanID("bbbbbbbbbbbbbbbb"),
		testutil.WithTraceID("abcdef01234567890abcdef012345678"),
		testutil.WithTimestamp(now.Add(-2*time.Hour)),
	)
	st.Merge(builder.Build(ev2))

	// Window: last 30 minutes
	result := BuildTopology(st.Snapshot(), now.Add(-30*time.Minute), now.Add(time.Minute))

	// Only svc-a and svc-b should appear (not svc-old/svc-ancient)
	if len(result.Nodes) != 2 {
		t.Errorf("expected 2 nodes within window, got %d", len(result.Nodes))
	}
}

func TestToCytoscapeFormat(t *testing.T) {
	result := TopologyResult{
		Nodes: []TopologyNode{
			{ID: "svc-a", Label: "svc-a", Status: "healthy", Invocations: 10, Errors: 0, ErrorRate: 0},
			{ID: "svc-b", Label: "svc-b", Status: "degraded", Invocations: 20, Errors: 3, ErrorRate: 0.15},
		},
		Edges: []TopologyEdge{
			{Source: "svc-a", Target: "svc-b", Requests: 15, Failures: 3},
		},
	}

	cy := ToCytoscapeFormat(result)

	if len(cy.Nodes) != 2 {
		t.Fatalf("expected 2 cytoscape nodes, got %d", len(cy.Nodes))
	}
	if len(cy.Edges) != 1 {
		t.Fatalf("expected 1 cytoscape edge, got %d", len(cy.Edges))
	}

	// Check node data fields
	n0 := cy.Nodes[0].Data
	if n0["id"] != "svc-a" {
		t.Errorf("node id: got %v, want svc-a", n0["id"])
	}
	if n0["type"] != "service" {
		t.Errorf("node type: got %v, want service", n0["type"])
	}
	if n0["invocations"] != 10 {
		t.Errorf("node invocations: got %v, want 10", n0["invocations"])
	}

	// Check edge data fields (Cytoscape format uses "count" and "label":"calls")
	e0 := cy.Edges[0].Data
	if e0["source"] != "svc-a" {
		t.Errorf("edge source: got %v, want svc-a", e0["source"])
	}
	if e0["target"] != "svc-b" {
		t.Errorf("edge target: got %v, want svc-b", e0["target"])
	}
	if e0["count"] != 15 {
		t.Errorf("edge count: got %v, want 15", e0["count"])
	}
	if e0["label"] != "calls" {
		t.Errorf("edge label: got %v, want calls", e0["label"])
	}
}
