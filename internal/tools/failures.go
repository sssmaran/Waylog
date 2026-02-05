package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

type failuresInput struct {
	Tier string `json:"tier"`
}

type failureEntry struct {
	RequestID string `json:"request_id"`
	TraceID   string `json:"trace_id,omitempty"`
	LatencyMs any    `json:"latency_ms,omitempty"`
	Tier      string `json:"tier,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type failuresOutput struct {
	Failures []failureEntry `json:"failures"`
}

func handleFailures(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input failuresInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}

	g := store.Snapshot()
	var out []failureEntry

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		req, ok := g.Nodes[e.From]
		if !ok || req.Type != core.NodeRequest {
			continue
		}

		var userTier string
		for _, ed := range g.Edges {
			if ed.From == req.ID && ed.Type == core.EdgeRequestBy {
				user, ok := g.Nodes[ed.To]
				if ok && user.Attr != nil {
					userTier, _ = user.Attr["tier"].(string)
				}
				break
			}
		}

		if input.Tier != "" && userTier != input.Tier {
			continue
		}

		errNode := g.Nodes[e.To]
		errorCode := ""
		if errNode.Attr != nil {
			errorCode, _ = errNode.Attr["code"].(string)
		}

		var latency any
		if req.Attr != nil {
			latency = req.Attr["latency_ms"]
		}

		traceID := ""
		if req.Attr != nil {
			if tid, ok := req.Attr["trace_id"].(string); ok {
				traceID = tid
			}
		}

		out = append(out, failureEntry{
			RequestID: req.ID,
			TraceID:   traceID,
			LatencyMs: latency,
			Tier:      userTier,
			ErrorCode: errorCode,
		})
	}

	return failuresOutput{Failures: out}, nil
}

type patternsInput struct {
	Window string `json:"window,omitempty"`
}

type patternsOutput struct {
	Patterns []analysis.FailurePattern `json:"patterns"`
}

func handleFailurePatterns(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input patternsInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}

	if input.Window != "" {
		d, err := time.ParseDuration(input.Window)
		if err != nil {
			return nil, fmt.Errorf("invalid window: %w", err)
		}
		end := time.Now()
		start := end.Add(-d)
		sum := store.SummarizeWindow(start, end)
		patterns := analysis.DetectFailurePatternsFromSummary(sum)
		g := store.Snapshot()
		for i := range patterns {
			patterns[i].ErrorCode = errorCodeForID(g, patterns[i].ErrorCode)
		}
		return patternsOutput{Patterns: patterns}, nil
	}

	g := store.Snapshot()
	patterns := analysis.DetectFailurePatterns(g)
	return patternsOutput{Patterns: patterns}, nil
}

type blastInput struct {
	ErrorCode       string `json:"error_code"`
	IncludeServices bool   `json:"include_services,omitempty"`
	TopUsers        int    `json:"top_users,omitempty"`
	ByTier          bool   `json:"by_tier,omitempty"`
}

type blastService struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
}

type blastTier struct {
	Tier  string `json:"tier"`
	Count int    `json:"count"`
}

type blastUser struct {
	UserID string `json:"user_id"`
	Count  int    `json:"count"`
}

type blastOutput struct {
	ErrorCode        string         `json:"error_code"`
	AffectedRequests int            `json:"affected_requests"`
	AffectedUsers    int            `json:"affected_users"`
	Services         []blastService `json:"services,omitempty"`
	Tiers            []blastTier    `json:"tiers,omitempty"`
	TopUsers         []blastUser    `json:"top_users,omitempty"`
	FeatureFlags     []string       `json:"feature_flags,omitempty"`
}

func handleBlastRadius(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input blastInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.ErrorCode == "" {
		return nil, fmt.Errorf("error_code required")
	}

	g := store.Snapshot()
	spanToRequest := map[string]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeRequestHasSpan {
			spanToRequest[e.To] = e.From
		}
	}

	requests := map[string]bool{}
	users := map[string]int{}
	services := map[string]int{}
	tiers := map[string]int{}
	flags := map[string]bool{}

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}

		errNode := g.Nodes[e.To]
		code := ""
		if errNode.Attr != nil {
			code, _ = errNode.Attr["code"].(string)
		}
		if code != input.ErrorCode {
			continue
		}

		reqID := e.From
		if fromNode, ok := g.Nodes[e.From]; ok && fromNode.Type == core.NodeSpan {
			if parentReq, ok := spanToRequest[e.From]; ok {
				reqID = parentReq
			} else {
				continue
			}
		}

		requests[reqID] = true

		for _, ed := range g.Edges {
			if ed.From != reqID {
				continue
			}

			switch ed.Type {
			case core.EdgeRequestBy:
				u := g.Nodes[ed.To]
				users[u.ID]++
				if t, ok := u.Attr["tier"].(string); ok {
					tiers[t]++
				}
			case core.EdgeHandledBy:
				s := g.Nodes[ed.To]
				services[serviceNameForNode(s)]++
			case core.EdgeUsedFlag:
				f := g.Nodes[ed.To]
				if name, ok := f.Attr["name"].(string); ok {
					flags[name] = true
				}
			}
		}
	}

	out := blastOutput{
		ErrorCode:        input.ErrorCode,
		AffectedRequests: len(requests),
		AffectedUsers:    len(users),
		FeatureFlags:     sortedKeys(flags),
	}

	if input.IncludeServices {
		out.Services = mapCountToSortedServices(services)
	}
	if input.ByTier {
		out.Tiers = mapCountToSortedTiers(tiers)
	}
	if input.TopUsers > 0 {
		out.TopUsers = mapCountToTopUsers(users, input.TopUsers)
	}

	return out, nil
}

type chainInput struct {
	RequestID string `json:"request_id"`
}

type chainOutput struct {
	RequestID string   `json:"request_id"`
	Services  []string `json:"services"`
}

func handleFailureChain(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input chainInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.RequestID == "" {
		return nil, fmt.Errorf("request_id required")
	}

	g := store.Snapshot()
	var serviceID string
	for _, e := range g.Edges {
		if e.From == input.RequestID && e.Type == core.EdgeHandledBy {
			serviceID = e.To
			break
		}
	}
	if serviceID == "" {
		return chainOutput{RequestID: input.RequestID, Services: []string{}}, nil
	}

	visited := map[string]bool{}
	var services []string
	curr := serviceID

	for {
		if visited[curr] {
			break
		}
		visited[curr] = true

		svc, ok := g.Nodes[curr]
		if !ok {
			break
		}
		services = append(services, serviceNameForNode(svc))

		next := ""
		for _, e := range g.Edges {
			if e.From == curr && e.Type == core.EdgeCalls {
				next = e.To
				break
			}
		}
		if next == "" {
			break
		}
		curr = next
	}

	return chainOutput{RequestID: input.RequestID, Services: services}, nil
}
