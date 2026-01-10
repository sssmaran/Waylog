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
// ensureGraphLocked guarantees s.graph is non-nil.
// Call ONLY while holding s.mu.
func (s *Store) ensureGraphLocked() {
	if s.graph == nil {
		s.graph = New()
	}
}

// Merge merges another graph into this store.
// Node IDs are deterministic, so duplicates are avoided.
// Edges are append-only.
func (s *Store) Merge(g *Graph) {
	if g == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
		s.ensureGraphLocked()


	// Merge nodes
	for id, node := range g.Nodes {
		if _, exists := s.graph.Nodes[id]; !exists {
			s.graph.Nodes[id] = node
		}
	}

	// Merge edges
	s.graph.Edges = append(s.graph.Edges, g.Edges...)
}

// Graph returns the live graph pointer.
// IMPORTANT: callers MUST treat this as read-only.
func (s *Store) Graph() *Graph {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGraphLocked()


	return s.graph
}

// Snapshot returns a deep copy of the graph.
// This is safe for persistence, debugging, and CLI reads.
func (s *Store) Snapshot() *Graph {
	s.mu.Lock()
	defer s.mu.Unlock()

	// imp! graph must never be nil
	s.ensureGraphLocked()

	// Deep copy nodes
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


// Restore replaces the current graph with a defensive copy of g.
// This avoids memory aliasing with snapshot data.
func (s *Store) Restore(g *Graph) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if g == nil {
		s.graph = New()
		return
	}

	// Deep-copy nodes
	nodes := make(map[string]Node, len(g.Nodes))
	for id, n := range g.Nodes {
		var attrCopy map[string]any
		if n.Attr != nil {
			attrCopy = make(map[string]any, len(n.Attr))
			for k, v := range n.Attr {
				attrCopy[k] = v
			}
		}

		nodes[id] = Node{
			ID:   n.ID,
			Type: n.Type,
			Attr: attrCopy,
		}
	}

	// Copy edges
	edges := make([]Edge, len(g.Edges))
	copy(edges, g.Edges)

	s.graph = &Graph{
		Nodes: nodes,
		Edges: edges,
	}
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
