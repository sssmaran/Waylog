package store

import (
	"sort"
	"time"
)

type WindowSummary struct {
	Start time.Time
	End   time.Time

	TotalRequests       int
	TotalFailures       int
	ServiceRequestCount map[string]int
	FlagRequestCount    map[string]int
	LatencyP50          int64
	LatencyP95          int64
	LatencyP99          int64

	ErrorCount        map[string]int
	ServiceErrorCount map[string]map[string]int
	FlagErrorCount    map[string]map[string]int
}

func (s *Store) SummarizeWindow(start, end time.Time) WindowSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := WindowSummary{
		Start:               start,
		End:                 end,
		ErrorCount:          map[string]int{},
		ServiceErrorCount:   map[string]map[string]int{},
		FlagErrorCount:      map[string]map[string]int{},
		ServiceRequestCount: map[string]int{},
		FlagRequestCount:    map[string]int{},
	}

	var latencies []int64

	for _, f := range s.requestFacts {
		t := f.SeenAt
		if t.IsZero() {
			continue // skip malformed facts
		}
		if t.Before(start) || t.After(end) {
			continue
		}

		out.TotalRequests++
		if len(f.Errors) > 0 {
			out.TotalFailures++
		}
		latencies = append(latencies, f.LatencyMs)

		seenSvc := map[string]bool{}
		for _, svcID := range f.Services {
			if !seenSvc[svcID] {
				seenSvc[svcID] = true
				out.ServiceRequestCount[svcID]++
			}
		}
		seenFlag := map[string]bool{}
		for _, flagID := range f.Flags {
			if !seenFlag[flagID] {
				seenFlag[flagID] = true
				out.FlagRequestCount[flagID]++
			}
		}

		for _, errID := range f.Errors {
			out.ErrorCount[errID]++

			for svcID := range seenSvc {
				m := out.ServiceErrorCount[svcID]
				if m == nil {
					m = map[string]int{}
					out.ServiceErrorCount[svcID] = m
				}
				m[errID]++
			}

			for flagID := range seenFlag {
				m := out.FlagErrorCount[flagID]
				if m == nil {
					m = map[string]int{}
					out.FlagErrorCount[flagID] = m
				}
				m[errID]++
			}
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	out.LatencyP50 = percentile(latencies, 50)
	out.LatencyP95 = percentile(latencies, 95)
	out.LatencyP99 = percentile(latencies, 99)

	return out
}
func percentile(sorted []int64, pct int) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	// Nearest-rank: idx = ceil(pct/100 * n) - 1
	idx := (pct*n + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return sorted[idx-1]
}

func (s *Store) ForEachRequestFact(
	start, end time.Time,
	fn func(RequestFacts),
) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, f := range s.requestFacts {
		if f.SeenAt.IsZero() {
			continue
		}
		if f.SeenAt.Before(start) || f.SeenAt.After(end) {
			continue
		}
		fn(f)
	}
}
