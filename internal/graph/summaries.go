package graph

import "time"

type WindowSummary struct {
	Start time.Time
	End   time.Time

	ErrorCount        map[string]int
	ServiceErrorCount map[string]map[string]int
	FlagErrorCount    map[string]map[string]int
}

func (s *Store) SummarizeWindow(start, end time.Time) WindowSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := WindowSummary{
		Start:             start,
		End:               end,
		ErrorCount:        map[string]int{},
		ServiceErrorCount: map[string]map[string]int{},
		FlagErrorCount:    map[string]map[string]int{},
	}

	for _, f := range s.requestFacts {
		t := f.SeenAt
		if t.IsZero() {
			continue // skip malformed facts
		}
		if t.Before(start) || t.After(end) {
			continue
		}


		for _, errID := range f.Errors {
			out.ErrorCount[errID]++

			for _, svcID := range f.Services {
				m := out.ServiceErrorCount[svcID]
				if m == nil {
					m = map[string]int{}
					out.ServiceErrorCount[svcID] = m
				}
				m[errID]++
			}

			for _, flagID := range f.Flags {
				m := out.FlagErrorCount[flagID]
				if m == nil {
					m = map[string]int{}
					out.FlagErrorCount[flagID] = m
				}
				m[errID]++
			}
		}
	}

	return out
}
