package core

import (
	"strings"
	"time"
)

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

// ServiceFromNode extracts the canonical service name from a request node.
// Prefers root_service (set by root span merge), falls back to event_name prefix.
func ServiceFromNode(n Node) string {
	if n.Attr == nil {
		return ""
	}
	if svc, ok := n.Attr["root_service"].(string); ok && svc != "" {
		return svc
	}
	if name, ok := n.Attr["event_name"].(string); ok {
		if idx := strings.IndexByte(name, '.'); idx > 0 {
			return name[:idx]
		}
	}
	return ""
}
