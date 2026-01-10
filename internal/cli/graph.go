package cli

import (
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
)

func runGraph(args []string) {
	if len(args) == 0 {
		fmt.Println("usage: waylog graph failures [--tier=premium]")
		return
	}
	g := graphStore().Graph()
	println("nodes:", len(g.Nodes), "edges:", len(g.Edges))

	switch args[0] {
	case "stats":
		handleStats()
	case "failures":
		handleFailures(args[1:])
	case "explain":
		handleExplain(args[1:])
	case "patterns":
		handlePatterns()
	default:
		fmt.Println("unknown graph command")
	}
}

func handleFailures(args []string) {
	var tier string
	for _, a := range args {
		if len(a) > 7 && a[:7] == "--tier=" {
			tier = a[7:]
		}
	}
	g := graphStore().Graph()

	for _, e := range g.Edges {
	if e.Type != graph.EdgeFailedWith {
		continue
	}

	req, ok := g.Nodes[e.From]
	if !ok {
		continue
	}

	var userID string
	for _, ed := range g.Edges {
		if ed.From == req.ID && ed.Type == graph.EdgeRequestBy {
			userID = ed.To
			break
		}
	}

	user, ok := g.Nodes[userID]
	if !ok {
		continue
	}

	if tier != "" && user.Attr["tier"] != tier {
		continue
	}

	fmt.Printf(
		"request=%s latency=%v tier=%v error=%s\n",
		req.ID,
		req.Attr["latency_ms"],
		user.Attr["tier"],
		g.Nodes[e.To].Attr["code"],
	)
}}

func handleExplain(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: waylog graph explain <request-id>")
		return
	}

	reqID := args[0]
	g := graphStore().Graph()

	ex, err := graph.ExplainRequest(g, reqID)
	if err != nil {
		fmt.Println("explain error:", err)
		return
	}

	fmt.Println("Request failed because:")

	if ex.UserTier != nil {
		fmt.Printf("- User tier: %v\n", ex.UserTier)
	}
	if ex.Flow != nil {
		fmt.Printf("- Flow: %v\n", ex.Flow)
	}
	if len(ex.FeatureFlags) > 0 {
		fmt.Printf("- Feature flags: %v\n", ex.FeatureFlags)
	}
	if ex.Service != nil {
		fmt.Printf("- Service: %v\n", ex.Service)
	}
	if ex.ErrorCode != nil {
		fmt.Printf("- Error code: %v\n", ex.ErrorCode)
	}
	if ex.ErrorMsg != nil {
		fmt.Printf("- Error message: %v\n", ex.ErrorMsg)
	}
	if ex.LatencyMs != nil {
		fmt.Printf("- Latency(ms): %v\n", ex.LatencyMs)
	}
}

func handlePatterns() {
	g := graphStore().Graph()

	patterns := graph.DetectFailurePatterns(g)

	if len(patterns) == 0 {
		fmt.Println("no failure patterns detected")
		return
	}

	fmt.Println("Failure patterns detected:")

	for _, p := range patterns {
		fmt.Printf(
			"- count=%d error=%s tier=%s flow=%s flags=%v\n",
			p.Count,
			p.ErrorCode,
			p.UserTier,
			p.Flow,
			p.FeatureFlags,
		)
	}
}

func handleStats() {
	store := graphStore()
	g := store.Graph()

	var requests, failures int

	for _, n := range g.Nodes {
		if n.Type == graph.NodeRequest {
			requests++
		}
		if n.Type == graph.NodeError {
			failures++
		}
	}

	fmt.Println("Graph stats:")
	fmt.Printf("- nodes: %d\n", len(g.Nodes))
	fmt.Printf("- edges: %d\n", len(g.Edges))
	fmt.Printf("- requests: %d\n", requests)
	fmt.Printf("- failures: %d\n", failures)
}


func graphStore() *graph.Store {
	return ingest.GlobalGraphStore
}
