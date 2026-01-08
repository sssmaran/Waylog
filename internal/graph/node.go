package graph

type NodeType string

const (
	NodeRequest NodeType = "request"
	NodeUser    NodeType = "user"
	NodeService NodeType = "service"
	NodeFlag    NodeType = "feature_flag"
	NodeError   NodeType = "error"
)

type Node struct {
	ID   string
	Type NodeType
	Attr map[string]any
}
