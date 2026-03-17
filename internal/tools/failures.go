package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
)

type failuresInput struct {
	Tier   string `json:"tier"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type failureEntry struct {
	RequestID string `json:"request_id"`
	TraceID   string `json:"trace_id,omitempty"`
	LatencyMs any    `json:"latency_ms,omitempty"`
	Tier      string `json:"tier,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type failuresOutput struct {
	SchemaVersion string         `json:"schema_version"`
	Failures      []failureEntry `json:"failures"`
	TotalCount    int            `json:"total_count"`
	HasMore       bool           `json:"has_more"`
}

func handleFailures(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input failuresInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
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
		for _, ed := range g.OutEdges[req.ID] {
			if ed.Type == core.EdgeRequestBy {
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

	page, totalCount, hasMore := applyPagination(out, input.Limit, input.Offset)
	return failuresOutput{SchemaVersion: "1.0", Failures: page, TotalCount: totalCount, HasMore: hasMore}, nil
}

type patternsInput struct {
	Window string `json:"window,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type patternsOutput struct {
	SchemaVersion string                    `json:"schema_version"`
	Patterns      []analysis.FailurePattern `json:"patterns"`
	TotalCount    int                       `json:"total_count"`
	HasMore       bool                      `json:"has_more"`
}

func handleFailurePatterns(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input patternsInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
		}
	}

	if input.Window != "" {
		d, err := time.ParseDuration(input.Window)
		if err != nil {
			return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid window: %v", err)}
		}
		end := time.Now()
		start := end.Add(-d)
		sum := store.SummarizeWindow(start, end)
		patterns := analysis.DetectFailurePatternsFromSummary(sum)
		g := store.Snapshot()
		for i := range patterns {
			patterns[i].ErrorCode = errorCodeForID(g, patterns[i].ErrorCode)
		}
		page, totalCount, hasMore := applyPagination(patterns, input.Limit, input.Offset)
		return patternsOutput{SchemaVersion: "1.0", Patterns: page, TotalCount: totalCount, HasMore: hasMore}, nil
	}

	g := store.Snapshot()
	patterns := analysis.DetectFailurePatterns(g)
	page, totalCount, hasMore := applyPagination(patterns, input.Limit, input.Offset)
	return patternsOutput{SchemaVersion: "1.0", Patterns: page, TotalCount: totalCount, HasMore: hasMore}, nil
}

type blastInput struct {
	ErrorCode       string `json:"error_code"`
	IncludeServices bool   `json:"include_services,omitempty"`
	TopUsers        int    `json:"top_users,omitempty"`
	ByTier          bool   `json:"by_tier,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Offset          int    `json:"offset,omitempty"`
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
	SchemaVersion    string         `json:"schema_version"`
	ErrorCode        string         `json:"error_code"`
	AffectedRequests int            `json:"affected_requests"`
	AffectedUsers    int            `json:"affected_users"`
	VIPUsers         int            `json:"vip_users"`
	SeverityScore    float64        `json:"severity_score"`
	Services         []blastService `json:"services,omitempty"`
	Tiers            []blastTier    `json:"tiers,omitempty"`
	TopUsers         []blastUser    `json:"top_users,omitempty"`
	FeatureFlags     []string       `json:"feature_flags,omitempty"`
	TotalCount       int            `json:"total_count"`
	HasMore          bool           `json:"has_more"`
}

func handleBlastRadius(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input blastInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.ErrorCode == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "error_code required"}
	}

	g := store.Snapshot()

	// Try error index for fast lookup
	var requestIDs []string
	useIndex := false
	ids, ready := store.ErrorIndex(input.ErrorCode)
	if ready {
		requestIDs = ids
		useIndex = true
	}

	weightRequest := config.GetenvFloat("BLAST_WEIGHT_REQUEST", 1.0)
	weightVIP := config.GetenvFloat("BLAST_WEIGHT_VIP", 10.0)
	weightPremium := config.GetenvFloat("BLAST_WEIGHT_PREMIUM", 3.0)
	weightService := config.GetenvFloat("BLAST_WEIGHT_SERVICE", 5.0)

	requests := map[string]bool{}
	users := map[string]int{}
	services := map[string]int{}
	tiers := map[string]int{}
	flags := map[string]bool{}
	vipUsers := map[string]bool{}
	premiumUsers := map[string]bool{}

	if useIndex {
		for _, reqID := range requestIDs {
			if requests[reqID] {
				continue
			}
			requests[reqID] = true
			collectBlastNeighbors(g, reqID, users, services, tiers, flags, vipUsers, premiumUsers)
		}
	} else {
		spanToRequest := spanToRequestIndex(g)
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
			reqID, ok := requestIDForFailureEdge(g, e, spanToRequest)
			if !ok {
				continue
			}
			if requests[reqID] {
				continue
			}
			requests[reqID] = true
			collectBlastNeighbors(g, reqID, users, services, tiers, flags, vipUsers, premiumUsers)
		}
	}

	out := blastOutput{
		SchemaVersion:    "1.0",
		ErrorCode:        input.ErrorCode,
		AffectedRequests: len(requests),
		AffectedUsers:    len(users),
		VIPUsers:         len(vipUsers),
		FeatureFlags:     sortedKeys(flags),
	}
	out.SeverityScore = float64(out.AffectedRequests)*weightRequest +
		float64(out.VIPUsers)*weightVIP +
		float64(len(premiumUsers))*weightPremium +
		float64(len(services))*weightService

	if input.IncludeServices {
		allServices := mapCountToSortedServices(services)
		out.TotalCount = len(allServices)
		var svcHasMore bool
		out.Services, _, svcHasMore = applyPagination(allServices, input.Limit, input.Offset)
		out.HasMore = svcHasMore
	} else {
		out.TotalCount = out.AffectedRequests
	}
	if input.ByTier {
		out.Tiers = mapCountToSortedTiers(tiers)
	}
	if input.TopUsers > 0 {
		out.TopUsers = mapCountToTopUsers(users, input.TopUsers)
	}

	return out, nil
}

func collectBlastNeighbors(g *core.Graph, reqID string, users map[string]int, services map[string]int, tiers map[string]int, flags map[string]bool, vipUsers map[string]bool, premiumUsers map[string]bool) {
	for _, ed := range g.OutEdges[reqID] {
		switch ed.Type {
		case core.EdgeRequestBy:
			u := g.Nodes[ed.To]
			users[u.ID]++
			if t, ok := u.Attr["tier"].(string); ok {
				tiers[t]++
				if t == "premium" {
					premiumUsers[u.ID] = true
				}
			}
			if vip, ok := u.Attr["vip"].(bool); ok && vip {
				vipUsers[u.ID] = true
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

type chainInput struct {
	RequestID string `json:"request_id"`
}

type chainOutput struct {
	SchemaVersion string   `json:"schema_version"`
	RequestID     string   `json:"request_id"`
	Services      []string `json:"services"`
}

func handleFailureChain(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input chainInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, &ToolError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)}
	}
	if input.RequestID == "" {
		return nil, &ToolError{Code: CodeInvalidParams, Message: "request_id required"}
	}

	g := store.Snapshot()
	var serviceID string
	for _, e := range g.OutEdges[input.RequestID] {
		if e.Type == core.EdgeHandledBy {
			serviceID = e.To
			break
		}
	}
	if serviceID == "" {
		return chainOutput{SchemaVersion: "1.0", RequestID: input.RequestID, Services: []string{}}, nil
	}

	visited := map[string]bool{}
	var svcs []string
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
		svcs = append(svcs, serviceNameForNode(svc))

		next := ""
		for _, e := range g.OutEdges[curr] {
			if e.Type == core.EdgeCalls {
				next = e.To
				break
			}
		}
		if next == "" {
			break
		}
		curr = next
	}

	return chainOutput{SchemaVersion: "1.0", RequestID: input.RequestID, Services: svcs}, nil
}
