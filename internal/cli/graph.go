package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/query"
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
		handlePatterns(args[1:])
	case "blast":
		handleBlast(args[1:])
	case "chain":
		handleChain(args[1:])
	case "query":
		handleQuery(args[1:])
	case "diff":
		handleDiff(args[1:])
	case "trace":
		handleTrace(args[1:])

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
		if e.Type != core.EdgeFailedWith {
			continue
		}

		req, ok := g.Nodes[e.From]
		if !ok {
			continue
		}

		var userID string
		for _, ed := range g.Edges {
			if ed.From == req.ID && ed.Type == core.EdgeRequestBy {
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
	}
}

func handleExplain(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: waylog graph explain <request-id>")
		return
	}

	reqID := args[0]
	g := graphStore().Graph()

	ex, err := analysis.ExplainRequest(g, reqID)
	if err != nil {
		fmt.Println("explain error:", err)
		return
	}

	fmt.Println("Request failed because:")

	if ex.SpanID != "" {
		fmt.Printf(
			"- Span: %v (%s)\n",
			ex.SpanService,
			ex.SpanDepth,
		)
	}

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

func handlePatterns(args []string) {
	var window string

	for _, a := range args {
		if strings.HasPrefix(a, "--window=") {
			window = strings.TrimPrefix(a, "--window=")
		}
	}

	store := graphStore()

	//window+summary based (fast-lookup)
	if window != "" {
		d, err := time.ParseDuration(window)
		if err != nil {
			fmt.Println("invalid --window value:", err)
			return
		}

		end := time.Now()
		start := end.Add(-d)

		sum := store.SummarizeWindow(start, end)
		patterns := analysis.DetectFailurePatternsFromSummary(sum)

		if len(patterns) == 0 {
			fmt.Println("no failure patterns detected")
			return
		}

		fmt.Println("Failure patterns detected (fast windowed):")
		for _, p := range patterns {
			fmt.Printf(
				"- count=%d error=%s\n",
				p.Count,
				p.ErrorCode,
			)
		}
		return
	}

	//full-graph scan
	g := graphStore().Snapshot()

	patterns := analysis.DetectFailurePatterns(g)

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

	var (
		requests int
		users    int
		services int
		flags    int
		failures int
	)

	for _, n := range g.Nodes {
		switch n.Type {
		case core.NodeRequest:
			requests++
		case core.NodeUser:
			users++
		case core.NodeService:
			services++
		case core.NodeFlag:
			flags++
		case core.NodeError:
			failures++
		}
	}

	fmt.Println("Graph stats:")

	fmt.Println("Entities:")
	fmt.Printf("- Requests: %d\n", requests)
	fmt.Println("  • Unique request traces retained after sampling")

	fmt.Printf("- Users: %d\n", users)
	fmt.Println("  • Distinct users involved in requests")
	fmt.Println("  • Derived from request → user relationships")

	fmt.Printf("- Services: %d\n", services)
	fmt.Println("  • Backend services that handled requests")

	fmt.Printf("- Feature Flags: %d\n", flags)
	fmt.Println("  • Runtime flags influencing request behavior")

	fmt.Printf("- Failure Types: %d\n", failures)
	fmt.Println("  • Unique error codes encountered in failed requests")

	fmt.Println("Relationships:")
	fmt.Printf("- Total Nodes: %d\n", len(g.Nodes))
	fmt.Printf("- Total Edges: %d\n", len(g.Edges))
	fmt.Println("  • Edges represent relationships such as:")
	fmt.Println("      request → user")
	fmt.Println("      request → service")
	fmt.Println("      request → error")
	fmt.Println("      request → feature flag")
}

type blastOptions struct {
	showServices bool
	topUsers     int
	byTier       bool
}

func parseBlastFlags(args []string) blastOptions {
	opts := blastOptions{}

	for _, a := range args {
		if a == "--services" {
			opts.showServices = true
		}
		if strings.HasPrefix(a, "--top-users=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(a, "--top-users=")); err == nil {
				opts.topUsers = n
			}
		}
		if a == "--by-tier" {
			opts.byTier = true
		}
	}

	return opts
}

func handleBlast(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: graph blast <ERROR_CODE> [--services] [--top-users=N] [--by-tier]")
		return
	}

	errorCode := args[0]
	opts := parseBlastFlags(args[1:])
	g := graphStore().Graph()

	requests := map[string]bool{}
	users := map[string]int{}
	services := map[string]int{}
	tiers := map[string]int{}
	flags := map[string]bool{}

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}

		errNode := g.Nodes[e.To]
		if errNode.Attr["code"] != errorCode {
			continue
		}

		reqID := e.From
		requests[reqID] = true

		for _, ed := range g.Edges {
			if ed.From != reqID {
				continue
			}

			switch ed.Type {
			case core.EdgeRequestBy:
				u := g.Nodes[ed.To]
				users[u.ID]++
				if t, ok := u.Attr["tier"].(string); ok {
					tiers[t]++
				}

			case core.EdgeHandledBy:
				s := g.Nodes[ed.To]
				services[s.ID]++

			case core.EdgeUsedFlag:
				f := g.Nodes[ed.To]
				if name, ok := f.Attr["name"].(string); ok {
					flags[name] = true
				}
			}
		}
	}

	// Summary
	fmt.Printf("\nBlast radius for error: %s\n\n", errorCode)
	fmt.Printf("Affected Requests: %d\n", len(requests))
	fmt.Printf("Affected Users: %d\n", len(users))

	if opts.showServices {
		fmt.Println("\nAffected Services:")
		for s, c := range services {
			fmt.Printf("- %s (%d requests)\n", s, c)
		}
	}

	if opts.byTier {
		fmt.Println("\nImpact by Tier:")
		for t, c := range tiers {
			fmt.Printf("- %s: %d users\n", t, c)
		}
	}

	if opts.topUsers > 0 {
		fmt.Println("\nTop Affected Users:")
		type pair struct {
			id string
			n  int
		}
		var list []pair
		for id, n := range users {
			list = append(list, pair{id, n})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].n > list[j].n
		})
		for i := 0; i < len(list) && i < opts.topUsers; i++ {
			fmt.Printf("- %s (%d failures)\n", list[i].id, list[i].n)
		}
	}

	if len(flags) > 0 {
		fmt.Println("\nCorrelated Feature Flags:")
		for f := range flags {
			fmt.Printf("- %s\n", f)
		}
	}
}

func handleChain(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: graph chain <request-id>")
		return
	}

	reqID := args[0]
	g := graphStore().Graph()

	// Step 1: find service handling this request
	var serviceID string
	for _, e := range g.Edges {
		if e.From == reqID && e.Type == core.EdgeHandledBy {
			serviceID = e.To
			break
		}
	}

	if serviceID == "" {
		fmt.Println("no service found for request")
		return
	}

	fmt.Println("Failure chain:")

	visited := make(map[string]bool)
	curr := serviceID

	for {
		if visited[curr] {
			// safety guard against cycles
			break
		}
		visited[curr] = true

		svc, ok := g.Nodes[curr]
		if !ok {
			break
		}

		// 🔑 Decode meaning instead of printing ID
		name := svc.Attr["service"]
		if name == nil {
			name = svc.Attr["name"] // fallback
		}

		fmt.Printf("- service=%v\n", name)

		// Step 2: walk to downstream service (if present)
		next := ""
		for _, e := range g.Edges {
			if e.From == curr && e.Type == core.EdgeCalls {
				next = e.To
				break
			}
		}

		if next == "" {
			break
		}
		curr = next
	}
}
func handleQuery(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: graph query '<expr>' --window=5m")
		return
	}

	expr := args[0]
	var window string

	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--window=") {
			window = strings.TrimPrefix(a, "--window=")
		}
	}

	if window == "" {
		fmt.Println("--window is required")
		return
	}

	d, err := time.ParseDuration(window)
	if err != nil {
		fmt.Println("invalid window:", err)
		return
	}

	pred, err := query.Parse(expr)
	if err != nil {
		fmt.Println("query parse error:", err)
		return
	}

	end := time.Now()
	start := end.Add(-d)

	store := graphStore()
	matched := 0

	store.ForEachRequestFact(start, end, func(f graphstore.RequestFacts) {
		if pred.Eval(f) {
			matched++
		}
	})

	fmt.Printf("Matched requests: %d\n", matched)
}

func handleDiff(args []string) {
	var (
		current  string
		baseline string
		offset   string
	)

	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--current="):
			current = strings.TrimPrefix(a, "--current=")
		case strings.HasPrefix(a, "--baseline="):
			baseline = strings.TrimPrefix(a, "--baseline=")
		case strings.HasPrefix(a, "--offset="):
			offset = strings.TrimPrefix(a, "--offset=")
		}
	}

	if current == "" || baseline == "" || offset == "" {
		fmt.Println("usage: graph diff --current=5m --baseline=5m --offset=1h")
		return
	}

	currDur, err := time.ParseDuration(current)
	if err != nil {
		fmt.Println("invalid --current:", err)
		return
	}
	baseDur, err := time.ParseDuration(baseline)
	if err != nil {
		fmt.Println("invalid --baseline:", err)
		return
	}
	offDur, err := time.ParseDuration(offset)
	if err != nil {
		fmt.Println("invalid --offset:", err)
		return
	}

	now := time.Now()

	currEnd := now
	currStart := currEnd.Add(-currDur)

	baseEnd := currEnd.Add(-offDur)
	baseStart := baseEnd.Add(-baseDur)

	store := graphStore()

	curr := store.SummarizeWindow(currStart, currEnd)
	base := store.SummarizeWindow(baseStart, baseEnd)

	diff := analysis.DiffSummaries(base, curr)

	printDiff(diff)
}
func printDiff(d analysis.WindowDiff) {
	if len(d.New) > 0 {
		fmt.Println("\nNew errors:")
		for _, e := range d.New {
			fmt.Printf("- %s (+%d)\n", e.ErrorCode, e.After)
		}
	}

	if len(d.Increased) > 0 {
		fmt.Println("\nIncreased:")
		for _, e := range d.Increased {
			fmt.Printf("- %s %+d (%d → %d)\n",
				e.ErrorCode, e.Delta, e.Before, e.After)
		}
	}

	if len(d.Decreased) > 0 {
		fmt.Println("\nDecreased:")
		for _, e := range d.Decreased {
			fmt.Printf("- %s %+d (%d → %d)\n",
				e.ErrorCode, e.Delta, e.Before, e.After)
		}
	}

	if len(d.Removed) > 0 {
		fmt.Println("\nRemoved:")
		for _, e := range d.Removed {
			fmt.Printf("- %s (-%d)\n", e.ErrorCode, e.Before)
		}
	}
}

func handleTrace(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: graph trace <trace-id>")
		return
	}

	traceID := args[0]
	g := graphStore().Graph()

	reqID := core.ID("request", traceID)
	fmt.Printf("Trace %s\n", traceID)

	// find root spans
	for _, e := range g.Edges {
		if e.Type != core.EdgeRequestHasSpan || e.From != reqID {
			continue
		}
		printSpanTree(g, e.To, 0)
	}
}

func printSpanTree(g *core.Graph, spanID string, depth int) {
	n, ok := g.Nodes[spanID]
	if !ok {
		return
	}

	indent := strings.Repeat("  ", depth)
	fmt.Printf("%s• span=%v service=%v\n",
		indent,
		n.Attr["span_id"],
		n.Attr["service"],
	)

	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf && e.To == spanID {
			printSpanTree(g, e.From, depth+1)
		}
	}
}

func graphStore() *graphstore.Store {
	return ingest.GlobalGraphStore
}
