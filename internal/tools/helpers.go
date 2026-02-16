package tools

import (
	"fmt"
	"sort"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// rootSpanIDsForTrace finds root spans (spans with no parent) for a given request.
func rootSpanIDsForTrace(g *core.Graph, reqID string) []string {
	hasParent := map[string]bool{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			hasParent[e.From] = true
		}
	}
	var roots []string
	seen := map[string]bool{}
	for _, e := range g.Edges {
		if e.Type != core.EdgeRequestHasSpan || e.From != reqID {
			continue
		}
		if seen[e.To] {
			continue
		}
		seen[e.To] = true
		if !hasParent[e.To] {
			roots = append(roots, e.To)
		}
	}
	return roots
}

// spanPathsForRoots builds service paths from root spans.
func spanPathsForRoots(g *core.Graph, roots []string) [][]string {
	if len(roots) == 0 {
		return nil
	}
	children := map[string][]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			children[e.To] = append(children[e.To], e.From)
		}
	}

	var paths [][]string
	for _, root := range roots {
		dfsSpanPaths(g, root, children, nil, &paths)
	}
	return paths
}

func dfsSpanPaths(g *core.Graph, spanID string, children map[string][]string, prefix []string, out *[][]string) {
	n, ok := g.Nodes[spanID]
	if !ok {
		return
	}
	service := ""
	if n.Attr != nil {
		if s, ok := n.Attr["service"].(string); ok {
			service = s
		}
	}
	if service == "" {
		service = spanID
	}
	path := append(prefix, service)
	kids := children[spanID]
	if len(kids) == 0 {
		*out = append(*out, path)
		return
	}
	for _, child := range kids {
		dfsSpanPaths(g, child, children, path, out)
	}
}

// serviceChainForRequest returns the chain of services for a request.
func serviceChainForRequest(g *core.Graph, reqID string) []string {
	serviceID := ""
	for _, e := range g.Edges {
		if e.From == reqID && e.Type == core.EdgeHandledBy {
			serviceID = e.To
			break
		}
	}
	if serviceID == "" {
		return nil
	}
	visited := map[string]bool{}
	var services []string
	curr := serviceID
	for {
		if visited[curr] {
			break
		}
		visited[curr] = true
		svc, ok := g.Nodes[curr]
		if !ok {
			break
		}
		services = append(services, serviceNameForNode(svc))
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
	return services
}

func spanToRequestIndex(g *core.Graph) map[string]string {
	index := map[string]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeRequestHasSpan {
			index[e.To] = e.From
		}
	}
	return index
}

func requestIDForFailureEdge(g *core.Graph, edge core.Edge, spanToRequest map[string]string) (string, bool) {
	fromNode, ok := g.Nodes[edge.From]
	if !ok {
		return "", false
	}
	switch fromNode.Type {
	case core.NodeRequest:
		return edge.From, true
	case core.NodeSpan:
		reqID, ok := spanToRequest[edge.From]
		if !ok || reqID == "" {
			return "", false
		}
		return reqID, true
	default:
		return "", false
	}
}

// errorCodeForID returns the error code attribute for an error node ID.
func errorCodeForID(g *core.Graph, id string) string {
	n, ok := g.Nodes[id]
	if !ok || n.Attr == nil {
		return id
	}
	if code, ok := n.Attr["code"].(string); ok && code != "" {
		return code
	}
	return id
}

// serviceNameForID returns the service name for a service node ID.
func serviceNameForID(g *core.Graph, id string) string {
	n, ok := g.Nodes[id]
	if !ok {
		return id
	}
	return serviceNameForNode(n)
}

// serviceNameForNode extracts the service name from a node's attributes.
func serviceNameForNode(n core.Node) string {
	if n.Attr == nil {
		return n.ID
	}
	if name, ok := n.Attr["service"]; ok && name != nil {
		return fmt.Sprintf("%v", name)
	}
	if name, ok := n.Attr["name"]; ok && name != nil {
		return fmt.Sprintf("%v", name)
	}
	return n.ID
}

// sortedKeys returns sorted keys from a map.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mapCountToSortedServices(m map[string]int) []blastService {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	out := make([]blastService, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, blastService{Service: p.name, Count: p.count})
	}
	return out
}

func mapCountToSortedTiers(m map[string]int) []blastTier {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	out := make([]blastTier, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, blastTier{Tier: p.name, Count: p.count})
	}
	return out
}

func mapCountToTopUsers(m map[string]int, n int) []blastUser {
	type pair struct {
		id    string
		count int
	}
	var pairs []pair
	for id, count := range m {
		pairs = append(pairs, pair{id: id, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]blastUser, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, blastUser{UserID: pairs[i].id, Count: pairs[i].count})
	}
	return out
}

func mapDiffEntries(g *core.Graph, entries []analysis.DiffEntry) []diffEntry {
	out := make([]diffEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, diffEntry{
			ErrorCode: errorCodeForID(g, e.ErrorCode),
			Before:    e.Before,
			After:     e.After,
			Delta:     e.Delta,
		})
	}
	return out
}

func mapCountToTopErrors(m map[string]int, n int) []insightError {
	type pair struct {
		code  string
		count int
	}
	var pairs []pair
	for code, count := range m {
		pairs = append(pairs, pair{code: code, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]insightError, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, insightError{ErrorCode: pairs[i].code, Count: pairs[i].count})
	}
	return out
}

func mapCountToTopServices(m map[string]int, n int) []insightService {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]insightService, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, insightService{Service: pairs[i].name, Count: pairs[i].count})
	}
	return out
}
