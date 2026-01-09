package graph

import "fmt"

// FailurePattern represents a recurring failure shape in the system.
type FailurePattern struct {
	ErrorCode    string
	Flow         string
	UserTier     string
	FeatureFlags []string

	Count int
}

// DetectFailurePatterns scans the graph and groups failed requests
// by shared causal attributes.
func DetectFailurePatterns(g *Graph) []FailurePattern {
	patterns := map[string]*FailurePattern{}

	for _, e := range g.Edges {
	if e.Type != EdgeFailedWith {
		continue
	}

	req, ok := g.Nodes[e.From]
	if !ok {
		continue
	}

	errNode, ok := g.Nodes[e.To]
	if !ok {
		continue
	}

	var (
		errorCode string
		flow      string
		userTier  string
		flags     []string
	)

	if errNode.Attr != nil {
		errorCode, _ = errNode.Attr["code"].(string)
	}

	if req.Attr != nil {
		flow, _ = req.Attr["flow"].(string)
	}

	// request -> user
	for _, ed := range g.Edges {
		if ed.From == req.ID && ed.Type == EdgeRequestBy {
			user, ok := g.Nodes[ed.To]
			if ok && user.Attr != nil {
				userTier, _ = user.Attr["tier"].(string)
			}
			break
		}
	}

	// request -> flags
	for _, ed := range g.Edges {
		if ed.From == req.ID && ed.Type == EdgeUsedFlag {
			flag, ok := g.Nodes[ed.To]
			if ok && flag.Attr != nil {
				if name, ok := flag.Attr["name"].(string); ok {
					flags = append(flags, name)
				}
			}
		}
	}

	key := fmt.Sprintf("%s|%s|%s|%v", errorCode, flow, userTier, flags)

	if _, ok := patterns[key]; !ok {
		patterns[key] = &FailurePattern{
			ErrorCode:    errorCode,
			Flow:         flow,
			UserTier:     userTier,
			FeatureFlags: flags,
		}
	}

	patterns[key].Count++
}


	var out []FailurePattern
	for _, p := range patterns {
		out = append(out, *p)
	}

	return out
}
