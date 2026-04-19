package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
)

type failuresInput struct {
	Tier   string `json:"tier"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type failureEntry struct {
	RequestID   string `json:"request_id"`
	TraceID     string `json:"trace_id,omitempty"`
	LatencyMs   any    `json:"latency_ms,omitempty"`
	Tier        string `json:"tier,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	ErrorPath   string `json:"error_path,omitempty"`
	ErrorReason string `json:"error_reason,omitempty"`
	RetryOf     int    `json:"retry_of,omitempty"`
}

type failuresOutput struct {
	SchemaVersion string         `json:"schema_version"`
	Failures      []failureEntry `json:"failures"`
	TotalCount    int            `json:"total_count"`
	HasMore       bool           `json:"has_more"`
}

type failureRecord struct {
	entry  failureEntry
	seenAt time.Time
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
	now := time.Now()
	var out []failureRecord

	store.ForEachRequestFact(time.Time{}, now, func(f graphstore.RequestFacts) {
		userTier := f.UserTier
		if input.Tier != "" && userTier != input.Tier {
			return
		}

		errorCodes := uniqueStrings(f.Errors)
		if len(errorCodes) == 0 {
			errorCodes = requestErrorCodesFromGraph(g, f.RequestID)
		}
		if len(errorCodes) == 0 {
			return
		}

		traceID := f.TraceID
		if traceID == "" {
			traceID = traceIDForRequest(g, f.RequestID)
		}
		latency := any(f.LatencyMs)
		errPath, errReason, retryOf := requestErrorContext(g, f.RequestID)
		for _, errorCode := range errorCodes {
			out = append(out, failureRecord{
				entry: failureEntry{
					RequestID:   f.RequestID,
					TraceID:     traceID,
					LatencyMs:   latency,
					Tier:        userTier,
					ErrorCode:   errorCode,
					ErrorPath:   errPath,
					ErrorReason: errReason,
					RetryOf:     retryOf,
				},
				seenAt: f.SeenAt,
			})
		}
	})

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].seenAt.Equal(out[j].seenAt) {
			return out[i].seenAt.After(out[j].seenAt)
		}
		if out[i].entry.RequestID != out[j].entry.RequestID {
			return out[i].entry.RequestID < out[j].entry.RequestID
		}
		return out[i].entry.ErrorCode < out[j].entry.ErrorCode
	})

	failures := make([]failureEntry, 0, len(out))
	for _, record := range out {
		failures = append(failures, record.entry)
	}

	page, totalCount, hasMore := applyPagination(failures, input.Limit, input.Offset)
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
		g := store.Snapshot()
		rollup := analysis.RollupWindow(g, store, traceStoreFrom(store), start, end)
		patterns := analysis.FailurePatternsFromRollup(rollup)
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
	ids, ready := store.ErrorIndex(input.ErrorCode)
	requestIDs := map[string]struct{}{}
	if ready {
		for _, id := range ids {
			requestIDs[id] = struct{}{}
		}
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
	now := time.Now()
	store.ForEachRequestFact(time.Time{}, now, func(f graphstore.RequestFacts) {
		if ready {
			if _, ok := requestIDs[f.RequestID]; !ok {
				return
			}
		} else if !f.HasError(input.ErrorCode) && !requestHasErrorCode(g, f.RequestID, input.ErrorCode) {
			return
		}

		requests[f.RequestID] = true
		collectBlastNeighbors(g, f, users, services, tiers, flags, vipUsers, premiumUsers)
	})

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

func collectBlastNeighbors(g *core.Graph, fact graphstore.RequestFacts, users map[string]int, services map[string]int, tiers map[string]int, flags map[string]bool, vipUsers map[string]bool, premiumUsers map[string]bool) {
	serviceNames := uniqueStrings(fact.Services)
	if len(serviceNames) == 0 {
		serviceNames = requestServicesFromGraph(g, fact.RequestID)
	}
	for _, name := range serviceNames {
		services[name]++
	}

	flagNames := uniqueStrings(fact.FeatureFlags)
	if len(flagNames) == 0 {
		flagNames = requestFeatureFlagsFromGraph(g, fact.RequestID)
	}
	for _, name := range flagNames {
		flags[name] = true
	}

	userID := fact.UserID
	userTier := fact.UserTier
	userRegion := fact.UserRegion
	userVIP := fact.UserVIP
	if userID == "" || userTier == "" || userRegion == "" {
		if fallbackID, fallbackTier, fallbackRegion, fallbackVIP, ok := requestUserInfoFromGraph(g, fact.RequestID); ok {
			if userID == "" {
				userID = fallbackID
			}
			if userTier == "" {
				userTier = fallbackTier
			}
			if userRegion == "" {
				userRegion = fallbackRegion
			}
			if !userVIP {
				userVIP = fallbackVIP
			}
		}
	}

	if userID != "" {
		users[userID]++
	}
	if userTier != "" {
		tiers[userTier]++
		if userTier == "premium" && userID != "" {
			premiumUsers[userID] = true
		}
	}
	if userVIP && userID != "" {
		vipUsers[userID] = true
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

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func traceIDForRequest(g *core.Graph, reqID string) string {
	if g == nil {
		return ""
	}
	req, ok := g.Nodes[reqID]
	if !ok || req.Attr == nil {
		return ""
	}
	traceID, _ := req.Attr["trace_id"].(string)
	return traceID
}

func requestErrorContext(g *core.Graph, reqID string) (path, reason string, retryOf int) {
	if g == nil {
		return
	}
	req, ok := g.Nodes[reqID]
	if !ok || req.Attr == nil {
		return
	}
	if p, ok := req.Attr["error_path"].(string); ok {
		path = p
	}
	if r, ok := req.Attr["error_reason"].(string); ok {
		reason = r
	}
	switch v := req.Attr["retry_of"].(type) {
	case int:
		retryOf = v
	case float64:
		retryOf = int(v)
	}
	return
}

func requestErrorCodesFromGraph(g *core.Graph, reqID string) []string {
	if g == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		errNode, ok := g.Nodes[e.To]
		if !ok || errNode.Attr == nil {
			continue
		}
		code, _ := errNode.Attr["code"].(string)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

func requestHasErrorCode(g *core.Graph, reqID, code string) bool {
	for _, current := range requestErrorCodesFromGraph(g, reqID) {
		if current == code {
			return true
		}
	}
	return false
}

func requestServicesFromGraph(g *core.Graph, reqID string) []string {
	if g == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeHandledBy {
			continue
		}
		svc, ok := g.Nodes[e.To]
		if !ok {
			continue
		}
		name := serviceNameForNode(svc)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func requestFeatureFlagsFromGraph(g *core.Graph, reqID string) []string {
	if g == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeUsedFlag {
			continue
		}
		flagNode, ok := g.Nodes[e.To]
		if !ok || flagNode.Attr == nil {
			continue
		}
		name, _ := flagNode.Attr["name"].(string)
		if name == "" {
			name = e.To
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func requestUserInfoFromGraph(g *core.Graph, reqID string) (userID, tier, region string, vip bool, ok bool) {
	if g == nil {
		return "", "", "", false, false
	}
	for _, e := range g.OutEdges[reqID] {
		if e.Type != core.EdgeRequestBy {
			continue
		}
		userNode, found := g.Nodes[e.To]
		if !found {
			return "", "", "", false, false
		}
		if userNode.Attr != nil {
			userID, _ = userNode.Attr["id"].(string)
			if userID == "" {
				userID = e.To
			}
			tier, _ = userNode.Attr["tier"].(string)
			region, _ = userNode.Attr["region"].(string)
			vip, _ = userNode.Attr["vip"].(bool)
		}
		return userID, tier, region, vip, true
	}
	return "", "", "", false, false
}
