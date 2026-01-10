package graph

import "sync"

type Store struct {
	mu    sync.Mutex
	graph *Graph
}

func NewStore() *Store {
	return &Store{
		graph: New(),
	}
}
func (s *Store) Merge(g *Graph) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Merge nodes (deterministic IDs prevent duplication)
	for id, node := range g.Nodes {
		if _, exists := s.graph.Nodes[id]; !exists {
			s.graph.Nodes[id] = node
		}
	}

	// Merge edges (append-only)
	s.graph.Edges = append(s.graph.Edges, g.Edges...)
}

func (s *Store) Graph() *Graph {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.graph
}

//graph snapshot for testing and debugging for persistence
func (s *Store) Snapshot() *Graph {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy nodes
	nodes := make(map[string]Node, len(s.graph.Nodes))
	for id, n := range s.graph.Nodes {
		var attr map[string]any
		if n.Attr != nil {
			attr = make(map[string]any, len(n.Attr))
			for k, v := range n.Attr {
				attr[k] = v
			}
		}
		n.Attr = attr
		nodes[id] = n
	}

	// Copy edges
	edges := make([]Edge, len(s.graph.Edges))
	copy(edges, s.graph.Edges)

	return &Graph{
		Nodes: nodes,
		Edges: edges,
	}
}

func (s *Store) Restore(g *Graph) {
	if g == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.graph = g
}
