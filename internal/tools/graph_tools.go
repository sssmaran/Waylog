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
