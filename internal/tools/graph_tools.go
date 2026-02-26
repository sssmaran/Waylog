package tools

import (
	"encoding/json"
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
		Examples:     []string{"show graph stats"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolExplainReqName,
		Description:  "Explain why a request failed using deterministic graph evidence.",
		InputSchema:  json.RawMessage(explainRequestInputSchema),
		OutputSchema: json.RawMessage(explainRequestOutputSchema),
		Handler:      handleExplainRequest,
		Examples:     []string{"explain request <trace-id>", "why did checkout fail"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolTraceGraphName,
		Description:  "Return the span tree for a trace ID from the graph snapshot.",
		InputSchema:  json.RawMessage(traceGraphInputSchema),
		OutputSchema: json.RawMessage(traceGraphOutputSchema),
		Handler:      handleTraceGraph,
		Examples:     []string{"show trace <trace-id>"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolTraceSummaryName,
		Description:  "Summarize a trace with request type, latency, and service path.",
		InputSchema:  json.RawMessage(traceSummaryInputSchema),
		OutputSchema: json.RawMessage(traceSummaryOutputSchema),
		Handler:      handleTraceSummary,
		Examples:     []string{"trace summary for <trace-id>"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolFailuresName,
		Description:  "List failed requests with optional tier filtering.",
		InputSchema:  json.RawMessage(failuresInputSchema),
		OutputSchema: json.RawMessage(failuresOutputSchema),
		Handler:      handleFailures,
		Examples:     []string{"list all failures"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolPatternsName,
		Description:  "Detect recurring failure patterns in the graph or a time window.",
		InputSchema:  json.RawMessage(patternsInputSchema),
		OutputSchema: json.RawMessage(patternsOutputSchema),
		Handler:      handleFailurePatterns,
		Examples:     []string{"show failure patterns in the last hour"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolBlastName,
		Description:  "Compute the blast radius for a specific error code.",
		InputSchema:  json.RawMessage(blastInputSchema),
		OutputSchema: json.RawMessage(blastOutputSchema),
		Handler:      handleBlastRadius,
		Examples:     []string{"what is the blast radius of PMT_502", "which users are affected"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolChainName,
		Description:  "Return the downstream service chain for a request.",
		InputSchema:  json.RawMessage(chainInputSchema),
		OutputSchema: json.RawMessage(chainOutputSchema),
		Handler:      handleFailureChain,
		Examples:     []string{"failure chain for request <trace-id>"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolQueryName,
		Description:  "Evaluate a query expression over a time window.",
		InputSchema:  json.RawMessage(queryInputSchema),
		OutputSchema: json.RawMessage(queryOutputSchema),
		Handler:      handleGraphQuery,
		Examples:     []string{"graph_query expr='error_code=PMT_502' window='10m'"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolDiffName,
		Description:  "Compare error counts between two time windows.",
		InputSchema:  json.RawMessage(diffInputSchema),
		OutputSchema: json.RawMessage(diffOutputSchema),
		Handler:      handleCompareWindows,
		Examples:     []string{"compare errors in last 10m vs 1h ago"},
	}); err != nil {
		return err
	}
	if err := reg.Register(Tool{
		Name:         toolInsightsName,
		Description:  "Summarize failures with top errors and services.",
		InputSchema:  json.RawMessage(insightsInputSchema),
		OutputSchema: json.RawMessage(insightsOutputSchema),
		Handler:      handleInsights,
		Examples:     []string{"show top errors", "what happened in the last 10 minutes"},
	}); err != nil {
		return err
	}
	return nil
}
