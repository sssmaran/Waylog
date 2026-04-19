package tools

import (
	"fmt"
	"sort"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

// serviceChainForRequest returns the chain of services for a request.
func serviceChainForRequest(g *core.Graph, reqID string) []string {
	serviceID := ""
	for _, e := range g.OutEdges[reqID] {
		if e.Type == core.EdgeHandledBy {
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
		for _, e := range g.OutEdges[curr] {
			if e.Type == core.EdgeCalls {
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
