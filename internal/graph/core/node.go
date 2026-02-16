package core

import "time"

type NodeType string

const (
	NodeRequest NodeType = "request"
	NodeUser    NodeType = "user"
	NodeService NodeType = "service"
	NodeFlag    NodeType = "feature_flag"
	NodeError   NodeType = "error"
	NodeSpan    NodeType = "span"
)

type Node struct {
	ID   string
	Type NodeType
	Attr map[string]any

	//for time-window commands
	FirstSeen time.Time
	LastSeen  time.Time
}
