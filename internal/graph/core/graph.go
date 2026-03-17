package core

type Graph struct {
	Nodes    map[string]Node
	Edges    []Edge
	OutEdges map[string][]Edge `json:"-"`
	InEdges  map[string][]Edge `json:"-"`
}

func New() *Graph {
	return &Graph{
		Nodes:    make(map[string]Node),
		Edges:    make([]Edge, 0),
		OutEdges: make(map[string][]Edge),
		InEdges:  make(map[string][]Edge),
	}
}

func (g *Graph) AddNode(n Node) {
	g.Nodes[n.ID] = n
}

func (g *Graph) AddEdge(e Edge) {
	g.Edges = append(g.Edges, e)
	if g.OutEdges == nil {
		g.OutEdges = make(map[string][]Edge)
	}
	if g.InEdges == nil {
		g.InEdges = make(map[string][]Edge)
	}
	g.OutEdges[e.From] = append(g.OutEdges[e.From], e)
	g.InEdges[e.To] = append(g.InEdges[e.To], e)
}

// RebuildIndexes rebuilds OutEdges and InEdges from the Edges slice.
// Call after deserializing or manually modifying Edges.
func (g *Graph) RebuildIndexes() {
	g.OutEdges = make(map[string][]Edge, len(g.Nodes))
	g.InEdges = make(map[string][]Edge, len(g.Nodes))
	for _, e := range g.Edges {
		g.OutEdges[e.From] = append(g.OutEdges[e.From], e)
		g.InEdges[e.To] = append(g.InEdges[e.To], e)
	}
}
