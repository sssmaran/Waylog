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

