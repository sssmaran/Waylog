package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/query"
)

const (
	toolGraphStatsName   = "graph_stats"
	toolExplainReqName   = "explain_request"
	toolTraceGraphName   = "trace_graph"
	toolTraceSummaryName = "trace_summary"
	toolFailuresName     = "graph_failures"
	toolPatternsName     = "failure_patterns"
	toolBlastName        = "blast_radius"
	toolChainName        = "failure_chain"
	toolQueryName        = "graph_query"
	toolDiffName         = "compare_windows"
	toolInsightsName     = "graph_insights"
)

func RegisterGraphTools(reg *Registry) error {
	if err := reg.Register(Tool{
		Name:         toolGraphStatsName,
		Description:  "Return entity and relationship counts for the current graph snapshot.",
		InputSchema:  json.RawMessage(graphStatsInputSchema),
		OutputSchema: json.RawMessage(graphStatsOutputSchema),
		Handler:      handleGraphStats,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolExplainReqName,
		Description:  "Explain why a request failed using deterministic graph evidence.",
		InputSchema:  json.RawMessage(explainRequestInputSchema),
		OutputSchema: json.RawMessage(explainRequestOutputSchema),
		Handler:      handleExplainRequest,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolTraceGraphName,
		Description:  "Return the span tree for a trace ID from the graph snapshot.",
		InputSchema:  json.RawMessage(traceGraphInputSchema),
		OutputSchema: json.RawMessage(traceGraphOutputSchema),
		Handler:      handleTraceGraph,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolTraceSummaryName,
		Description:  "Summarize a trace with request type, latency, and service path.",
		InputSchema:  json.RawMessage(traceSummaryInputSchema),
		OutputSchema: json.RawMessage(traceSummaryOutputSchema),
		Handler:      handleTraceSummary,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolFailuresName,
		Description:  "List failed requests with optional tier filtering.",
		InputSchema:  json.RawMessage(failuresInputSchema),
		OutputSchema: json.RawMessage(failuresOutputSchema),
		Handler:      handleFailures,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolPatternsName,
		Description:  "Detect recurring failure patterns in the graph or a time window.",
		InputSchema:  json.RawMessage(patternsInputSchema),
		OutputSchema: json.RawMessage(patternsOutputSchema),
		Handler:      handleFailurePatterns,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolBlastName,
		Description:  "Compute the blast radius for a specific error code.",
		InputSchema:  json.RawMessage(blastInputSchema),
		OutputSchema: json.RawMessage(blastOutputSchema),
		Handler:      handleBlastRadius,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolChainName,
		Description:  "Return the downstream service chain for a request.",
		InputSchema:  json.RawMessage(chainInputSchema),
		OutputSchema: json.RawMessage(chainOutputSchema),
		Handler:      handleFailureChain,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolQueryName,
		Description:  "Evaluate a query expression over a time window.",
		InputSchema:  json.RawMessage(queryInputSchema),
		OutputSchema: json.RawMessage(queryOutputSchema),
		Handler:      handleGraphQuery,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolDiffName,
		Description:  "Compare error counts between two time windows.",
		InputSchema:  json.RawMessage(diffInputSchema),
		OutputSchema: json.RawMessage(diffOutputSchema),
		Handler:      handleCompareWindows,
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolInsightsName,
		Description:  "Summarize failures with top errors and services.",
		InputSchema:  json.RawMessage(insightsInputSchema),
		OutputSchema: json.RawMessage(insightsOutputSchema),
		Handler:      handleInsights,
	}); err != nil {
		return err
	}
	return nil
}

type graphStatsOutput struct {
	Nodes        int `json:"nodes"`
	Edges        int `json:"edges"`
	Requests     int `json:"requests"`
	Users        int `json:"users"`
	Services     int `json:"services"`
	FeatureFlags int `json:"feature_flags"`
	Failures     int `json:"failures"`
}

func handleGraphStats(ctx context.Context, store Store, _ json.RawMessage) (any, error) {
	_ = ctx
	g := store.Snapshot()
	out := graphStatsOutput{
		Nodes: len(g.Nodes),
		Edges: len(g.Edges),
	}

	for _, n := range g.Nodes {
		switch n.Type {
		case core.NodeRequest:
			out.Requests++
		case core.NodeUser:
			out.Users++
		case core.NodeService:
			out.Services++
		case core.NodeFlag:
			out.FeatureFlags++
		case core.NodeError:
			out.Failures++
		}
	}

	return out, nil
}

type explainRequestInput struct {
	RequestID string `json:"request_id"`
	TraceID   string `json:"trace_id"`
}

type explainRequestOutput struct {
	RequestID    string   `json:"request_id"`
	LatencyMs    any      `json:"latency_ms,omitempty"`
	Flow         any      `json:"flow,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	UserTier     any      `json:"user_tier,omitempty"`
	FeatureFlags []string `json:"feature_flags,omitempty"`
	SpanID       string   `json:"span_id,omitempty"`
	SpanService  any      `json:"span_service,omitempty"`
	SpanDepth    string   `json:"span_depth,omitempty"`
	Service      any      `json:"service,omitempty"`
	ErrorCode    any      `json:"error_code,omitempty"`
	ErrorMsg     any      `json:"error_msg,omitempty"`
}

func handleExplainRequest(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input explainRequestInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.RequestID == "" && input.TraceID == "" {
		return nil, fmt.Errorf("request_id or trace_id required")
	}
	requestID := input.RequestID
	if requestID == "" {
		requestID = core.ID("request", input.TraceID)
	}
	g := store.Snapshot()
	ex, err := analysis.ExplainRequest(g, requestID)
	if err != nil {
		return nil, err
	}
	return explainRequestOutput{
		RequestID:    ex.RequestID,
		LatencyMs:    ex.LatencyMs,
		Flow:         ex.Flow,
		UserID:       ex.UserID,
		UserTier:     ex.UserTier,
		FeatureFlags: ex.FeatureFlags,
		SpanID:       ex.SpanID,
		SpanService:  ex.SpanService,
		SpanDepth:    ex.SpanDepth,
		Service:      ex.Service,
		ErrorCode:    ex.ErrorCode,
		ErrorMsg:     ex.ErrorMsg,
	}, nil
}

type traceGraphInput struct {
	TraceID string `json:"trace_id"`
}

type traceSpan struct {
	SpanID   string      `json:"span_id,omitempty"`
	Service  any         `json:"service,omitempty"`
	Children []traceSpan `json:"children,omitempty"`
}

type traceGraphOutput struct {
	TraceID string      `json:"trace_id"`
	Roots   []traceSpan `json:"roots"`
}

func handleTraceGraph(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceGraphInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.TraceID == "" {
		return nil, fmt.Errorf("trace_id required")
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)
	var roots []traceSpan

	for _, e := range g.Edges {
		if e.Type != core.EdgeRequestHasSpan || e.From != reqID {
			continue
		}
		roots = append(roots, buildTraceSpan(g, e.To, map[string]bool{}))
	}

	return traceGraphOutput{
		TraceID: input.TraceID,
		Roots:   roots,
	}, nil
}

func buildTraceSpan(g *core.Graph, spanID string, visited map[string]bool) traceSpan {
	if visited[spanID] {
		return traceSpan{}
	}
	visited[spanID] = true

	n, ok := g.Nodes[spanID]
	if !ok {
		return traceSpan{}
	}

	out := traceSpan{
		Service: n.Attr["service"],
	}
	if span, ok := n.Attr["span_id"]; ok && span != nil {
		out.SpanID = fmt.Sprintf("%v", span)
	}

	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf && e.To == spanID {
			out.Children = append(out.Children, buildTraceSpan(g, e.From, visited))
		}
	}

	return out
}

type traceSummaryInput struct {
	TraceID string `json:"trace_id"`
}

type traceSummaryOutput struct {
	TraceID     string     `json:"trace_id"`
	RequestID   string     `json:"request_id"`
	EventName   string     `json:"event_name,omitempty"`
	Flow        string     `json:"flow,omitempty"`
	LatencyMs   any        `json:"latency_ms,omitempty"`
	RootSpanIDs []string   `json:"root_span_ids,omitempty"`
	Paths       [][]string `json:"paths,omitempty"`
}

func handleTraceSummary(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input traceSummaryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.TraceID == "" {
		return nil, fmt.Errorf("trace_id required")
	}

	g := store.Snapshot()
	reqID := core.ID("request", input.TraceID)

	out := traceSummaryOutput{
		TraceID:   input.TraceID,
		RequestID: reqID,
	}

	if req, ok := g.Nodes[reqID]; ok {
		if req.Attr != nil {
			if name, ok := req.Attr["event_name"].(string); ok {
				out.EventName = name
			}
			if flow, ok := req.Attr["flow"].(string); ok {
				out.Flow = flow
			}
			out.LatencyMs = req.Attr["latency_ms"]
		}
	}

	rootSpans := rootSpanIDsForTrace(g, reqID)
	out.RootSpanIDs = rootSpans
	out.Paths = spanPathsForRoots(g, rootSpans)

	if len(out.Paths) == 0 {
		if chain := serviceChainForRequest(g, reqID); len(chain) > 0 {
			out.Paths = [][]string{chain}
		}
	}

	return out, nil
}

func rootSpanIDsForTrace(g *core.Graph, reqID string) []string {
	var roots []string
	for _, e := range g.Edges {
		if e.Type == core.EdgeRequestHasSpan && e.From == reqID {
			roots = append(roots, e.To)
		}
	}
	return roots
}

func spanPathsForRoots(g *core.Graph, roots []string) [][]string {
	if len(roots) == 0 {
		return nil
	}
	children := map[string][]string{}
	for _, e := range g.Edges {
		if e.Type == core.EdgeSpanChildOf {
			children[e.To] = append(children[e.To], e.From)
		}
	}

	var paths [][]string
	for _, root := range roots {
		dfsSpanPaths(g, root, children, nil, &paths)
	}
	return paths
}

func dfsSpanPaths(g *core.Graph, spanID string, children map[string][]string, prefix []string, out *[][]string) {
	n, ok := g.Nodes[spanID]
	if !ok {
		return
	}
	service := ""
	if n.Attr != nil {
		if s, ok := n.Attr["service"].(string); ok {
			service = s
		}
	}
	if service == "" {
		service = spanID
	}
	path := append(prefix, service)
	kids := children[spanID]
	if len(kids) == 0 {
		*out = append(*out, path)
		return
	}
	for _, child := range kids {
		dfsSpanPaths(g, child, children, path, out)
	}
}

func serviceChainForRequest(g *core.Graph, reqID string) []string {
	serviceID := ""
	for _, e := range g.Edges {
		if e.From == reqID && e.Type == core.EdgeHandledBy {
			serviceID = e.To
			break
		}
	}
	if serviceID == "" {
		return nil
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
	return services
}

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

type queryInput struct {
	Expr   string `json:"expr"`
	Window string `json:"window"`
}

type queryOutput struct {
	MatchedRequests int `json:"matched_requests"`
}

func handleGraphQuery(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input queryInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.Expr == "" {
		return nil, fmt.Errorf("expr required")
	}
	if input.Window == "" {
		return nil, fmt.Errorf("window required")
	}

	d, err := time.ParseDuration(input.Window)
	if err != nil {
		return nil, fmt.Errorf("invalid window: %w", err)
	}
	pred, err := query.Parse(input.Expr)
	if err != nil {
		return nil, fmt.Errorf("query parse error: %w", err)
	}

	end := time.Now()
	start := end.Add(-d)
	matched := 0

	store.ForEachRequestFact(start, end, func(f graphstore.RequestFacts) {
		if pred.Eval(f) {
			matched++
		}
	})

	return queryOutput{MatchedRequests: matched}, nil
}

type diffInput struct {
	Current  string `json:"current"`
	Baseline string `json:"baseline"`
	Offset   string `json:"offset"`
}

type diffEntry struct {
	ErrorCode string `json:"error_code"`
	Before    int    `json:"before,omitempty"`
	After     int    `json:"after,omitempty"`
	Delta     int    `json:"delta"`
}

type diffOutput struct {
	New       []diffEntry `json:"new,omitempty"`
	Removed   []diffEntry `json:"removed,omitempty"`
	Increased []diffEntry `json:"increased,omitempty"`
	Decreased []diffEntry `json:"decreased,omitempty"`
}

func handleCompareWindows(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input diffInput
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if input.Current == "" || input.Baseline == "" || input.Offset == "" {
		return nil, fmt.Errorf("current, baseline, and offset required")
	}

	currDur, err := time.ParseDuration(input.Current)
	if err != nil {
		return nil, fmt.Errorf("invalid current: %w", err)
	}
	baseDur, err := time.ParseDuration(input.Baseline)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline: %w", err)
	}
	offDur, err := time.ParseDuration(input.Offset)
	if err != nil {
		return nil, fmt.Errorf("invalid offset: %w", err)
	}

	now := time.Now()
	currEnd := now
	currStart := currEnd.Add(-currDur)
	baseEnd := currEnd.Add(-offDur)
	baseStart := baseEnd.Add(-baseDur)

	curr := store.SummarizeWindow(currStart, currEnd)
	base := store.SummarizeWindow(baseStart, baseEnd)
	diff := analysis.DiffSummaries(base, curr)
	g := store.Snapshot()

	return diffOutput{
		New:       mapDiffEntries(g, diff.New),
		Removed:   mapDiffEntries(g, diff.Removed),
		Increased: mapDiffEntries(g, diff.Increased),
		Decreased: mapDiffEntries(g, diff.Decreased),
	}, nil
}

type insightsInput struct {
	Window      string `json:"window,omitempty"`
	TopErrors   int    `json:"top_errors,omitempty"`
	TopServices int    `json:"top_services,omitempty"`
}

type insightError struct {
	ErrorCode string `json:"error_code"`
	Count     int    `json:"count"`
}

type insightService struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
}

type insightsOutput struct {
	TotalFailures int              `json:"total_failures"`
	TopErrors     []insightError   `json:"top_errors,omitempty"`
	TopServices   []insightService `json:"top_services,omitempty"`
}

func handleInsights(ctx context.Context, store Store, params json.RawMessage) (any, error) {
	_ = ctx
	var input insightsInput
	if len(params) > 0 {
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}

	if input.TopErrors == 0 {
		input.TopErrors = 5
	}
	if input.TopServices == 0 {
		input.TopServices = 5
	}

	g := store.Snapshot()

	if input.Window != "" {
		d, err := time.ParseDuration(input.Window)
		if err != nil {
			return nil, fmt.Errorf("invalid window: %w", err)
		}
		end := time.Now()
		start := end.Add(-d)
		sum := store.SummarizeWindow(start, end)

		errorCounts := map[string]int{}
		total := 0
		for errID, count := range sum.ErrorCount {
			code := errorCodeForID(g, errID)
			errorCounts[code] += count
			total += count
		}

		serviceCounts := map[string]int{}
		for svcID, errs := range sum.ServiceErrorCount {
			name := serviceNameForID(g, svcID)
			for _, count := range errs {
				serviceCounts[name] += count
			}
		}

		return insightsOutput{
			TotalFailures: total,
			TopErrors:     mapCountToTopErrors(errorCounts, input.TopErrors),
			TopServices:   mapCountToTopServices(serviceCounts, input.TopServices),
		}, nil
	}

	errorCounts := map[string]int{}
	serviceCounts := map[string]int{}
	total := 0

	for _, e := range g.Edges {
		if e.Type != core.EdgeFailedWith {
			continue
		}
		errNode := g.Nodes[e.To]
		code := ""
		if errNode.Attr != nil {
			code, _ = errNode.Attr["code"].(string)
		}
		if code == "" {
			code = e.To
		}
		errorCounts[code]++
		total++

		reqID := e.From
		if fromNode, ok := g.Nodes[e.From]; ok && fromNode.Type == core.NodeSpan {
			reqID = ""
			for _, re := range g.Edges {
				if re.Type == core.EdgeRequestHasSpan && re.To == e.From {
					reqID = re.From
					break
				}
			}
			if reqID == "" {
				continue
			}
		}

		for _, ed := range g.Edges {
			if ed.From == reqID && ed.Type == core.EdgeHandledBy {
				serviceCounts[serviceNameForID(g, ed.To)]++
			}
		}
	}

	return insightsOutput{
		TotalFailures: total,
		TopErrors:     mapCountToTopErrors(errorCounts, input.TopErrors),
		TopServices:   mapCountToTopServices(serviceCounts, input.TopServices),
	}, nil
}

func errorCodeForID(g *core.Graph, id string) string {
	n, ok := g.Nodes[id]
	if !ok || n.Attr == nil {
		return id
	}
	if code, ok := n.Attr["code"].(string); ok && code != "" {
		return code
	}
	return id
}

func serviceNameForID(g *core.Graph, id string) string {
	n, ok := g.Nodes[id]
	if !ok {
		return id
	}
	return serviceNameForNode(n)
}

func serviceNameForNode(n core.Node) string {
	if n.Attr == nil {
		return n.ID
	}
	if name, ok := n.Attr["service"]; ok && name != nil {
		return fmt.Sprintf("%v", name)
	}
	if name, ok := n.Attr["name"]; ok && name != nil {
		return fmt.Sprintf("%v", name)
	}
	return n.ID
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mapCountToSortedServices(m map[string]int) []blastService {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	out := make([]blastService, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, blastService{Service: p.name, Count: p.count})
	}
	return out
}

func mapCountToSortedTiers(m map[string]int) []blastTier {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	out := make([]blastTier, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, blastTier{Tier: p.name, Count: p.count})
	}
	return out
}

func mapCountToTopUsers(m map[string]int, n int) []blastUser {
	type pair struct {
		id    string
		count int
	}
	var pairs []pair
	for id, count := range m {
		pairs = append(pairs, pair{id: id, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]blastUser, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, blastUser{UserID: pairs[i].id, Count: pairs[i].count})
	}
	return out
}

func mapDiffEntries(g *core.Graph, entries []analysis.DiffEntry) []diffEntry {
	out := make([]diffEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, diffEntry{
			ErrorCode: errorCodeForID(g, e.ErrorCode),
			Before:    e.Before,
			After:     e.After,
			Delta:     e.Delta,
		})
	}
	return out
}

func mapCountToTopErrors(m map[string]int, n int) []insightError {
	type pair struct {
		code  string
		count int
	}
	var pairs []pair
	for code, count := range m {
		pairs = append(pairs, pair{code: code, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]insightError, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, insightError{ErrorCode: pairs[i].code, Count: pairs[i].count})
	}
	return out
}

func mapCountToTopServices(m map[string]int, n int) []insightService {
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range m {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].count > pairs[j].count
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]insightService, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, insightService{Service: pairs[i].name, Count: pairs[i].count})
	}
	return out
}

const graphStatsInputSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const graphStatsOutputSchema = `{
  "type": "object",
  "properties": {
    "nodes": { "type": "integer" },
    "edges": { "type": "integer" },
    "requests": { "type": "integer" },
    "users": { "type": "integer" },
    "services": { "type": "integer" },
    "feature_flags": { "type": "integer" },
    "failures": { "type": "integer" }
  },
  "required": ["nodes", "edges", "requests", "users", "services", "feature_flags", "failures"],
  "additionalProperties": false
}`

const explainRequestInputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" },
    "trace_id": { "type": "string" }
  },
  "additionalProperties": false
}`

const explainRequestOutputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" },
    "latency_ms": {},
    "flow": {},
    "user_id": { "type": "string" },
    "user_tier": {},
    "feature_flags": { "type": "array", "items": { "type": "string" } },
    "span_id": { "type": "string" },
    "span_service": {},
    "span_depth": { "type": "string" },
    "service": {},
    "error_code": {},
    "error_msg": {}
  },
  "required": ["request_id"],
  "additionalProperties": false
}`

const traceGraphInputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" }
  },
  "required": ["trace_id"],
  "additionalProperties": false
}`

const traceGraphOutputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" },
    "roots": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "span_id": { "type": "string" },
          "service": {},
          "children": { "type": "array" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["trace_id", "roots"],
  "additionalProperties": false
}`

const traceSummaryInputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" }
  },
  "required": ["trace_id"],
  "additionalProperties": false
}`

const traceSummaryOutputSchema = `{
  "type": "object",
  "properties": {
    "trace_id": { "type": "string" },
    "request_id": { "type": "string" },
    "event_name": { "type": "string" },
    "flow": { "type": "string" },
    "latency_ms": {},
    "root_span_ids": { "type": "array", "items": { "type": "string" } },
    "paths": {
      "type": "array",
      "items": { "type": "array", "items": { "type": "string" } }
    }
  },
  "required": ["trace_id", "request_id"],
  "additionalProperties": false
}`

const failuresInputSchema = `{
  "type": "object",
  "properties": {
    "tier": { "type": "string" }
  },
  "additionalProperties": false
}`

const failuresOutputSchema = `{
  "type": "object",
  "properties": {
    "failures": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "request_id": { "type": "string" },
          "trace_id": { "type": "string" },
          "latency_ms": {},
          "tier": { "type": "string" },
          "error_code": { "type": "string" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["failures"],
  "additionalProperties": false
}`

const patternsInputSchema = `{
  "type": "object",
  "properties": {
    "window": { "type": "string" }
  },
  "additionalProperties": false
}`

const patternsOutputSchema = `{
  "type": "object",
  "properties": {
    "patterns": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "error_code": { "type": "string" },
          "flow": { "type": "string" },
          "user_tier": { "type": "string" },
          "feature_flags": { "type": "array", "items": { "type": "string" } },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["patterns"],
  "additionalProperties": false
}`

const blastInputSchema = `{
  "type": "object",
  "properties": {
    "error_code": { "type": "string" },
    "include_services": { "type": "boolean" },
    "top_users": { "type": "integer" },
    "by_tier": { "type": "boolean" }
  },
  "required": ["error_code"],
  "additionalProperties": false
}`

const blastOutputSchema = `{
  "type": "object",
  "properties": {
    "error_code": { "type": "string" },
    "affected_requests": { "type": "integer" },
    "affected_users": { "type": "integer" },
    "services": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "service": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "tiers": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "tier": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "top_users": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "user_id": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "feature_flags": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["error_code", "affected_requests", "affected_users"],
  "additionalProperties": false
}`

const chainInputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" }
  },
  "required": ["request_id"],
  "additionalProperties": false
}`

const chainOutputSchema = `{
  "type": "object",
  "properties": {
    "request_id": { "type": "string" },
    "services": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["request_id", "services"],
  "additionalProperties": false
}`

const queryInputSchema = `{
  "type": "object",
  "properties": {
    "expr": { "type": "string" },
    "window": { "type": "string" }
  },
  "required": ["expr", "window"],
  "additionalProperties": false
}`

const queryOutputSchema = `{
  "type": "object",
  "properties": {
    "matched_requests": { "type": "integer" }
  },
  "required": ["matched_requests"],
  "additionalProperties": false
}`

const diffInputSchema = `{
  "type": "object",
  "properties": {
    "current": { "type": "string" },
    "baseline": { "type": "string" },
    "offset": { "type": "string" }
  },
  "required": ["current", "baseline", "offset"],
  "additionalProperties": false
}`

const diffOutputSchema = `{
  "type": "object",
  "properties": {
    "new": { "type": "array" },
    "removed": { "type": "array" },
    "increased": { "type": "array" },
    "decreased": { "type": "array" }
  },
  "additionalProperties": false
}`

const insightsInputSchema = `{
  "type": "object",
  "properties": {
    "window": { "type": "string" },
    "top_errors": { "type": "integer" },
    "top_services": { "type": "integer" }
  },
  "additionalProperties": false
}`

const insightsOutputSchema = `{
  "type": "object",
  "properties": {
    "total_failures": { "type": "integer" },
    "top_errors": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "error_code": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    },
    "top_services": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "service": { "type": "string" },
          "count": { "type": "integer" }
        },
        "additionalProperties": false
      }
    }
  },
  "required": ["total_failures"],
  "additionalProperties": false
}`
