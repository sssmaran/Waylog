package ingestv2

import (
	"sort"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type Reader struct {
	index *RecentIndex
}

func NewReader(index *RecentIndex) *Reader {
	return &Reader{index: index}
}

type SearchFilter struct {
	Service           string
	Statuses          map[eventv2.Status]struct{}
	ErrorCode         string
	TraceID           string
	Since             time.Time
	Until             time.Time
	IncludeSuppressed bool
}

type EventSearchResult struct {
	Events     []*eventv2.Event
	NextCursor *EventCursor
}

type TraceGetResult struct {
	TraceID string
	Events  []*eventv2.Event
	Linkage string
}

type TraceSummary = apiv2.TraceSummary

type RecentTracesResult struct {
	Traces     []TraceSummary
	NextCursor *TraceCursor
}

func (r *Reader) GetEvent(eventID string) (*eventv2.Event, bool) {
	if r == nil || r.index == nil {
		return nil, false
	}
	return r.index.GetByID(eventID)
}

func (r *Reader) SearchEvents(f SearchFilter, after *EventCursor, limit int) EventSearchResult {
	if r == nil || r.index == nil || limit <= 0 {
		return EventSearchResult{}
	}
	events := r.index.SnapshotEvents()
	filtered := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		if eventMatchesFilter(ev, f) {
			filtered = append(filtered, ev)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return compareEventSearchOrder(filtered[i], filtered[j]) < 0
	})

	out := make([]*eventv2.Event, 0, limit)
	hasMore := false
	for _, ev := range filtered {
		if ev == nil || !afterEventCursor(ev.TsStart.UnixNano(), ev.EventID, after) {
			continue
		}
		if len(out) == limit {
			hasMore = true
			break
		}
		out = append(out, cloneEvent(ev))
	}
	var next *EventCursor
	if hasMore && len(out) > 0 {
		last := out[len(out)-1]
		next = &EventCursor{TsNano: last.TsStart.UnixNano(), EventID: last.EventID}
	}
	return EventSearchResult{Events: out, NextCursor: next}
}

func (r *Reader) GetTrace(traceID string) (TraceGetResult, bool) {
	if r == nil || r.index == nil || traceID == "" {
		return TraceGetResult{}, false
	}
	events := r.index.SnapshotTrace(traceID)
	if len(events) == 0 {
		return TraceGetResult{}, false
	}
	ordered, linkage := OrderTrace(events)
	out := make([]*eventv2.Event, 0, len(ordered))
	for _, ev := range ordered {
		out = append(out, cloneEvent(ev))
	}
	return TraceGetResult{TraceID: traceID, Events: out, Linkage: linkage}, true
}

func (r *Reader) RecentTraces(f SearchFilter, after *TraceCursor, limit int) RecentTracesResult {
	if r == nil || r.index == nil || limit <= 0 {
		return RecentTracesResult{}
	}
	groups := r.index.SnapshotTraces()
	summaries := make([]TraceSummary, 0, len(groups))
	for traceID, events := range groups {
		if traceHasMatchingEvent(events, f) {
			summary, ok := buildTraceSummary(traceID, events, f.IncludeSuppressed)
			if ok {
				summaries = append(summaries, summary)
			}
		}
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if !summaries[i].TsStart.Equal(summaries[j].TsStart) {
			return summaries[i].TsStart.After(summaries[j].TsStart)
		}
		return summaries[i].TraceID < summaries[j].TraceID
	})

	out := make([]TraceSummary, 0, limit)
	hasMore := false
	for _, summary := range summaries {
		if !afterTraceCursor(summary.TsStart.UnixNano(), summary.TraceID, after) {
			continue
		}
		if len(out) == limit {
			hasMore = true
			break
		}
		out = append(out, summary)
	}
	var next *TraceCursor
	if hasMore && len(out) > 0 {
		last := out[len(out)-1]
		next = &TraceCursor{TsNano: last.TsStart.UnixNano(), TraceID: last.TraceID}
	}
	return RecentTracesResult{Traces: out, NextCursor: next}
}

func eventMatchesFilter(ev *eventv2.Event, f SearchFilter) bool {
	if ev == nil {
		return false
	}
	if f.Service != "" && ev.Service != f.Service {
		return false
	}
	if f.TraceID != "" && ev.TraceID != f.TraceID {
		return false
	}
	if f.ErrorCode != "" {
		if ev.Anchor == nil || ev.Anchor.ErrorCode != f.ErrorCode {
			return false
		}
	}
	if len(f.Statuses) > 0 {
		if _, ok := f.Statuses[ev.Status]; !ok {
			return false
		}
	} else if ev.Status == eventv2.StatusSuppressed && !f.IncludeSuppressed {
		return false
	}
	if !f.Since.IsZero() && ev.TsStart.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && ev.TsStart.After(f.Until) {
		return false
	}
	return true
}

func traceHasMatchingEvent(events []*eventv2.Event, f SearchFilter) bool {
	for _, ev := range events {
		if eventMatchesFilter(ev, f) {
			return true
		}
	}
	return false
}

func buildTraceSummary(traceID string, events []*eventv2.Event, includeSuppressed bool) (TraceSummary, bool) {
	if len(events) == 0 {
		return TraceSummary{}, false
	}
	ordered, _ := OrderTrace(events)
	if len(ordered) == 0 {
		return TraceSummary{}, false
	}
	var minStart, maxEnd time.Time
	services := make([]string, 0, len(ordered))
	seenServices := map[string]struct{}{}
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if minStart.IsZero() || ev.TsStart.Before(minStart) {
			minStart = ev.TsStart
		}
		if maxEnd.IsZero() || ev.TsEnd.After(maxEnd) {
			maxEnd = ev.TsEnd
		}
	}
	for _, ev := range ordered {
		if ev == nil || ev.Service == "" {
			continue
		}
		if _, ok := seenServices[ev.Service]; ok {
			continue
		}
		seenServices[ev.Service] = struct{}{}
		services = append(services, ev.Service)
	}
	resolved := ResolveAnchorWithOptions(events, ResolveOpts{ExcludeSuppressed: !includeSuppressed})
	if resolved.Event == nil {
		return TraceSummary{}, false
	}
	status := resolved.Event.Status
	duration := int64(0)
	if !minStart.IsZero() && !maxEnd.IsZero() && maxEnd.After(minStart) {
		duration = maxEnd.Sub(minStart).Milliseconds()
	}
	return TraceSummary{
		TraceID:       traceID,
		TsStart:       minStart,
		DurationMS:    duration,
		Services:      services,
		Status:        status,
		AnchorSummary: FormatEventErrorFamily(resolved.Event),
	}, true
}

func compareEventSearchOrder(a, b *eventv2.Event) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if !a.TsStart.Equal(b.TsStart) {
		if a.TsStart.After(b.TsStart) {
			return -1
		}
		return 1
	}
	if a.EventID < b.EventID {
		return -1
	}
	if a.EventID > b.EventID {
		return 1
	}
	return 0
}
