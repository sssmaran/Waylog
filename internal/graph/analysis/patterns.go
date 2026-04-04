package analysis

import (
	"fmt"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
)

// FailurePattern represents a recurring failure shape in the system.
type FailurePattern struct {
	ErrorCode    string   `json:"error_code"`
	Flow         string   `json:"flow"`
	UserTier     string   `json:"user_tier"`
	FeatureFlags []string `json:"feature_flags"`
	Count        int      `json:"count"`
}

// DetectFailurePatterns scans the graph and groups failed requests
// by shared causal attributes.
func DetectFailurePatterns(g *core.Graph) []FailurePattern {
	patterns := map[string]*FailurePattern{}

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}

		req, ok := g.Nodes[e.From]
		if !ok || req.Type != core.NodeRequest {
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

		if req.Attr != nil {
			userTier, _ = req.Attr["user_tier"].(string)
			if userTier == "" {
				userTier, _ = req.Attr["tier"].(string)
			}
			flags = append(flags, store.AttrToStringSlice(req.Attr["feature_flags"])...)
		}
		if len(flags) == 0 {
			for _, ed := range g.OutEdges[req.ID] {
				if ed.Type != core.EdgeUsedFlag {
					continue
				}
				flag, ok := g.Nodes[ed.To]
				if ok && flag.Attr != nil {
					if name, ok := flag.Attr["name"].(string); ok {
						flags = append(flags, name)
					}
				}
			}
		}

		if flags == nil {
			flags = []string{}
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

// DetectFailurePatternsFromSummary builds failure patterns
// using window summaries instead of graph traversal.
func DetectFailurePatternsFromSummary(sum store.WindowSummary) []FailurePattern {
	patterns := map[string]*FailurePattern{}

	for errID, count := range sum.ErrorCount {
		// NOTE:
		// At this stage we only know error + count.
		// Flow / tier / flags will be layered later (Module 6.4).
		key := errID

		if _, ok := patterns[key]; !ok {
			patterns[key] = &FailurePattern{
				ErrorCode: errID,
			}
		}
		patterns[key].Count += count
	}

	var out []FailurePattern
	for _, p := range patterns {
		out = append(out, *p)
	}
	return out
}
