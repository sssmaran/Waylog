package store

import (
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/window"
)

type Store struct {
	mu    sync.RWMutex
	graph *core.Graph
	//for fast lookups
	requestFacts map[string]RequestFacts
	seenRequests map[string]struct{}
	counters     *Counters
}

func NewStore() *Store {
	return &Store{
		graph: core.New(),
		requestFacts: map[string]RequestFacts{},
		seenRequests: map[string]struct{}{},
		counters:     NewCounters(),
	}
}
// ensureGraphLocked guarantees s.graph is non-nil.
// Call ONLY while holding s.mu.
func (s *Store) ensureGraphLocked() {
	if s.graph == nil {
		s.graph = core.New()
	}
}

// Merge merges another graph into this store.
// Node IDs are deterministic, so duplicates are avoided.
// Edges are append-only.
func (s *Store) Merge(g *core.Graph) {
	if g == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGraphLocked()


	// Merge nodes
	for id, incoming := range g.Nodes {
    existing, exists := s.graph.Nodes[id]
    if !exists {
        s.graph.Nodes[id] = incoming
        continue
    }

    // Merge time ranges (imp!)
    mergeNodeTime(&existing, &incoming)

    // merging attrs if needed later
    s.graph.Nodes[id] = existing
}
	// Merge edges
	s.graph.Edges = append(s.graph.Edges, g.Edges...)

	for id, n := range g.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if _, seen := s.seenRequests[id]; seen {
			continue
		}

		facts, ok := extractRequestFactsFromGraph(g, id)
		if !ok {
			continue
		}

		// Mark seen + store facts
		s.seenRequests[id] = struct{}{}
		s.requestFacts[id] = facts

		// Update all-time counters (optional but cheap)
		s.applyFactsToCountersLocked(facts)
	}
}

func (s *Store) applyFactsToCountersLocked(f RequestFacts) {
	// error counts
	for _, errID := range f.Errors {
		s.counters.ErrorCount[errID]++
		// service -> error
		for _, svcID := range f.Services {
			m := s.counters.ServiceErrorCount[svcID]
			if m == nil {
				m = map[string]int{}
				s.counters.ServiceErrorCount[svcID] = m
			}
			m[errID]++
		}
		// flag -> error
		for _, flagID := range f.Flags {
			m := s.counters.FlagErrorCount[flagID]
			if m == nil {
				m = map[string]int{}
				s.counters.FlagErrorCount[flagID] = m
			}
			m[errID]++
		}
	}

}

//helper for time-window commands 
// internal/graph/store.go

func mergeNodeTime(dst, src *core.Node) {
    if !src.FirstSeen.IsZero() &&
        (dst.FirstSeen.IsZero() || src.FirstSeen.Before(dst.FirstSeen)) {
        dst.FirstSeen = src.FirstSeen
    }

    if !src.LastSeen.IsZero() &&
        (dst.LastSeen.IsZero() || src.LastSeen.After(dst.LastSeen)) {
        dst.LastSeen = src.LastSeen
    }
}


// Graph returns the live graph pointer.
// IMPORTANT: callers MUST treat this as read-only.
func (s *Store) Graph() *core.Graph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureGraphLocked()


	return s.graph
}

// Snapshot returns a deep copy of the graph.
// This is safe for persistence, debugging, and CLI reads.
func (s *Store) Snapshot() *core.Graph {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// imp! graph must never be nil
	s.ensureGraphLocked()

	// Deep copy nodes
	nodes := make(map[string]core.Node, len(s.graph.Nodes))
for id, n := range s.graph.Nodes {
    var attr map[string]any
    if n.Attr != nil {
        attr = make(map[string]any, len(n.Attr))
        for k, v := range n.Attr {
            attr[k] = v
        }
    }

    nodes[id] = core.Node{
        ID:        n.ID,
        Type:      n.Type,
        Attr:      attr,
        FirstSeen: n.FirstSeen,
        LastSeen:  n.LastSeen,
    }
}


	// Copy edges
	edges := make([]core.Edge, len(s.graph.Edges))
	copy(edges, s.graph.Edges)

	return &core.Graph{
		Nodes: nodes,
		Edges: edges,
	}
}


// Restore replaces the current graph with a defensive copy of g.
// This avoids memory aliasing with snapshot data.
func (s *Store) Restore(g *core.Graph) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if g == nil {
		s.graph = core.New()
		s.rebuildDerivedIndexesLocked()
		return
	}

	// Deep-copy nodes
	nodes := make(map[string]core.Node, len(g.Nodes))
	for id, n := range g.Nodes {
		var attrCopy map[string]any
		if n.Attr != nil {
			attrCopy = make(map[string]any, len(n.Attr))
			for k, v := range n.Attr {
				attrCopy[k] = v
			}
		}

		nodes[id] = core.Node{
    ID:        n.ID,
    Type:      n.Type,
    Attr:      attrCopy,
    FirstSeen: n.FirstSeen,
    LastSeen:  n.LastSeen,
}

		
	}

	// Copy edges
	edges := make([]core.Edge, len(g.Edges))
	copy(edges, g.Edges)

	s.graph = &core.Graph{
		Nodes: nodes,
		Edges: edges,
	}
	s.backfillRequestTimestampsLocked()
	s.rebuildDerivedIndexesLocked()
}

// PruneOlderThan drops requests with LastSeen before cutoff.
// This keeps 1-hop context for remaining requests.
func (s *Store) PruneOlderThan(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureGraphLocked()
	s.graph = window.FilterByWindow(s.graph, cutoff, time.Now())
	s.rebuildDerivedIndexesLocked()
}

func (s *Store) rebuildDerivedIndexesLocked() {
	s.requestFacts = map[string]RequestFacts{}
	s.seenRequests = map[string]struct{}{}
	s.counters = NewCounters()

	for id, n := range s.graph.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		facts, ok := extractRequestFactsFromGraph(s.graph, id)
		if !ok {
			continue
		}
		s.seenRequests[id] = struct{}{}
		s.requestFacts[id] = facts
		s.applyFactsToCountersLocked(facts)
	}
}

func (s *Store) backfillRequestTimestampsLocked() {
	for id, n := range s.graph.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if !n.LastSeen.IsZero() {
			continue
		}
		ts := parseTimestampAttr(n.Attr)
		if ts.IsZero() {
			continue
		}
		n.FirstSeen = ts
		n.LastSeen = ts
		s.graph.Nodes[id] = n
	}
}

func parseTimestampAttr(attr map[string]any) time.Time {
	if attr == nil {
		return time.Time{}
	}
	if v, ok := attr["timestamp"]; ok {
		switch t := v.(type) {
		case time.Time:
			return t
		case string:
			if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return ts
			}
			if ts, err := time.Parse(time.RFC3339, t); err == nil {
				return ts
			}
		case float64:
			return time.Unix(int64(t), 0).UTC()
		case int64:
			return time.Unix(t, 0).UTC()
		case int:
			return time.Unix(int64(t), 0).UTC()
		}
	}
	return time.Time{}
}





func extractRequestFactsFromGraph(g *core.Graph, reqID string) (RequestFacts, bool) {
	reqNode, ok := g.Nodes[reqID]
	if !ok || reqNode.Type != core.NodeRequest {
		return RequestFacts{}, false
	}

	f := RequestFacts{
		RequestID: reqID,
		SeenAt:    reqNode.LastSeen,
	}

	// Gather neighbors from edges (1-hop around request)
	for _, e := range g.Edges {
		var otherID string
		if e.From == reqID {
			otherID = e.To
		} else if e.To == reqID {
			otherID = e.From
		} else {
			continue
		}

		n, ok := g.Nodes[otherID]
		if !ok {
			continue
		}

		switch n.Type {
		case core.NodeService:
			f.Services = append(f.Services, n.ID)
		case core.NodeError:
			f.Errors = append(f.Errors, n.ID)
		case core.NodeFlag:
			f.Flags = append(f.Flags, n.ID)
		}
	}

	return f, true
}








//prev version of store.go- which  deals with not a copy of graph but direct pointer manipulation


// package graph

// import "sync"

// type Store struct {
// 	mu    sync.Mutex
// 	graph *Graph
// }

// func NewStore() *Store {
// 	return &Store{
// 		graph: New(),
// 	}
// }
// func (s *Store) Merge(g *Graph) {
// 	s.mu.Lock()
// 	defer s.mu.Unlock()

// 	// Merge nodes (deterministic IDs prevent duplication)
// 	for id, node := range g.Nodes {
// 		if _, exists := s.graph.Nodes[id]; !exists {
// 			s.graph.Nodes[id] = node
// 		}
// 	}

// 	// Merge edges (append-only)
// 	s.graph.Edges = append(s.graph.Edges, g.Edges...)
// }

// func (s *Store) Graph() *Graph {
// 	s.mu.Lock()
// 	defer s.mu.Unlock()

// 	return s.graph
// }

// //graph snapshot for testing and debugging for persistence
// func (s *Store) Snapshot() *Graph {
// 	s.mu.Lock()
// 	defer s.mu.Unlock()

// 	// Copy nodes
// 	nodes := make(map[string]Node, len(s.graph.Nodes))
// 	for id, n := range s.graph.Nodes {
// 		var attr map[string]any
// 		if n.Attr != nil {
// 			attr = make(map[string]any, len(n.Attr))
// 			for k, v := range n.Attr {
// 				attr[k] = v
// 			}
// 		}
// 		n.Attr = attr
// 		nodes[id] = n
// 	}

// 	// Copy edges
// 	edges := make([]Edge, len(s.graph.Edges))
// 	copy(edges, s.graph.Edges)

// 	return &Graph{
// 		Nodes: nodes,
// 		Edges: edges,
// 	}
// }

// func (s *Store) Restore(g *Graph) {
// 	if g == nil {
// 		return
// 	}

// 	s.mu.Lock()
// 	defer s.mu.Unlock()
// 	s.graph = g
// }
