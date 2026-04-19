// Package analysis exposes deterministic, structure-only views over the hot
// graph and trace store. The contents are projections — never inference.
//
// # Canonical rollup contract
//
// [RollupWindow] is the SINGLE SOURCE OF TRUTH for default user-facing
// rollups. Any endpoint, tool, or detector that surfaces "top errors",
// "top services", "failure patterns", spike/anomaly summaries, or overview
// KPIs MUST consume RollupWindow. These surfaces count one root-cause error
// per failed request (see [RootCauseSpan] for the tie-break) instead of
// amplifying by propagated error spread.
//
// Detail surfaces that intentionally show spread — trace stories, blast
// radius, failure chains — keep propagation-counted semantics and consume
// store.SummarizeWindow directly. New user-facing default rollups must NOT
// introduce ad-hoc aggregation; add a field to RollupSummary instead.
package analysis

import (
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

// RollupSummary is the root-cause-counted window summary. Each failed request
// in [Start, End] contributes exactly one PrimaryErrorCount entry, one
// ServiceFailureCount entry per distinct service it touched, and one
// FlagFailureCount entry per distinct feature flag it carried.
//
// TotalRequests, ServiceRequestCount, FlagRequestCount, and latency
// percentiles retain the same "per-request" semantics as
// store.WindowSummary — the only behavioral difference is the error-side
// counting.
type RollupSummary struct {
	Start time.Time
	End   time.Time

	TotalRequests int
	TotalFailures int

	// PrimaryErrorCount counts the canonical root-cause error code for each
	// failed request exactly once. Replaces store.WindowSummary.ErrorCount
	// for all default user-facing rollups.
	PrimaryErrorCount map[string]int

	ServiceRequestCount map[string]int
	ServiceFailureCount map[string]int

	FlagRequestCount map[string]int
	FlagFailureCount map[string]int

	LatencyP50 int64
	LatencyP95 int64
	LatencyP99 int64
}

// RollupSource is the minimal request-fact producer RollupWindow needs.
// Both *store.Store and the tools-layer Store interface satisfy this.
type RollupSource interface {
	ForEachRequestFact(start, end time.Time, fn func(store.RequestFacts))
}

// RollupWindow computes root-cause-counted rollups for all requests seen
// between [start, end].
//
// For each failed request, RootCauseSpan picks a single primary error code
// (deepest → earliest → lex; trace store preferred, then graph, then
// request-level fallback). If even the request-level fallback finds nothing
// for a failed request — for instance during a partial replay — the request
// still counts toward TotalFailures but contributes nothing to
// PrimaryErrorCount.
func RollupWindow(g *core.Graph, s RollupSource, ts *tracestore.Store, start, end time.Time) RollupSummary {
	out := RollupSummary{
		Start:               start,
		End:                 end,
		PrimaryErrorCount:   map[string]int{},
		ServiceRequestCount: map[string]int{},
		ServiceFailureCount: map[string]int{},
		FlagRequestCount:    map[string]int{},
		FlagFailureCount:    map[string]int{},
	}

	if s == nil {
		return out
	}

	var latencies []int64
	s.ForEachRequestFact(start, end, func(f store.RequestFacts) {
		out.TotalRequests++
		latencies = append(latencies, f.LatencyMs)

		seenSvc := map[string]bool{}
		for _, svc := range f.Services {
			if svc == "" || seenSvc[svc] {
				continue
			}
			seenSvc[svc] = true
			out.ServiceRequestCount[svc]++
		}
		seenFlag := map[string]bool{}
		for _, flag := range f.FeatureFlags {
			if flag == "" || seenFlag[flag] {
				continue
			}
			seenFlag[flag] = true
			out.FlagRequestCount[flag]++
		}

		if len(f.Errors) == 0 {
			return
		}
		out.TotalFailures++

		for svc := range seenSvc {
			out.ServiceFailureCount[svc]++
		}
		for flag := range seenFlag {
			out.FlagFailureCount[flag]++
		}

		if g != nil {
			if _, code, ok := RootCauseSpan(g, ts, f.RequestID); ok && code != "" {
				out.PrimaryErrorCount[code]++
				return
			}
		}
		// RootCauseSpan found nothing — fall back to the first error on the
		// fact so the PrimaryErrorCount total remains close to TotalFailures
		// during replay or when graph lookup fails.
		for _, code := range f.Errors {
			if code != "" {
				out.PrimaryErrorCount[code]++
				return
			}
		}
	})

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	out.LatencyP50 = percentile(latencies, 50)
	out.LatencyP95 = percentile(latencies, 95)
	out.LatencyP99 = percentile(latencies, 99)
	return out
}

// percentile implements nearest-rank percentile on a pre-sorted slice.
// Mirrors the semantics of store.percentile so rollups and propagation
// summaries stay comparable.
func percentile(sorted []int64, pct int) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := (pct*n + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return sorted[idx-1]
}
