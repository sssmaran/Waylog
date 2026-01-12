package core

type Graph struct {
	Nodes map[string]Node
	Edges []Edge
}

func New() *Graph {
	return &Graph{
		Nodes: make(map[string]Node),
		Edges: make([]Edge, 0),
	}
}

func (g *Graph) AddNode(n Node) {
	g.Nodes[n.ID] = n
}

func (g *Graph) AddEdge(e Edge) {
	g.Edges = append(g.Edges, e)
}
