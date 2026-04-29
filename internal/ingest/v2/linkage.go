package ingestv2

import (
	"sort"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	LinkageCausal            = apiv2.LinkageCausal
	LinkageTimestampFallback = apiv2.LinkageTimestampFallback
)

func OrderTrace(events []*eventv2.Event) ([]*eventv2.Event, string) {
	if len(events) == 0 {
		return nil, LinkageTimestampFallback
	}
	graph := buildTraceGraph(events)
	if !graph.usable {
		return orderByTimestamp(events), LinkageTimestampFallback
	}

	roots := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		if ev != nil && ev.ParentSpanID == "" {
			roots = append(roots, ev)
		}
	}
	sortEventsStable(roots)

	seen := map[string]struct{}{}
	ordered := make([]*eventv2.Event, 0, len(events))
	var walk func(*eventv2.Event)
	walk = func(ev *eventv2.Event) {
		if ev == nil {
			return
		}
		if _, ok := seen[ev.EventID]; ok {
			return
		}
		seen[ev.EventID] = struct{}{}
		ordered = append(ordered, ev)
		children := append([]*eventv2.Event(nil), graph.children[ev.EventID]...)
		sortEventsStable(children)
		for _, child := range children {
			walk(child)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	if len(ordered) != len(events) {
		return orderByTimestamp(events), LinkageTimestampFallback
	}
	return ordered, LinkageCausal
}

type traceGraph struct {
	children map[string][]*eventv2.Event
	parents  map[string]string
	depths   map[string]int
	usable   bool
}

func buildTraceGraph(events []*eventv2.Event) traceGraph {
	byEventID := map[string]*eventv2.Event{}
	spanOwner := map[string]*eventv2.Event{}
	for _, ev := range events {
		if ev == nil || ev.EventID == "" {
			continue
		}
		byEventID[ev.EventID] = ev
		for _, step := range ev.Steps {
			if step.SpanID != "" {
				spanOwner[step.SpanID] = ev
			}
		}
	}

	g := traceGraph{
		children: map[string][]*eventv2.Event{},
		parents:  map[string]string{},
		depths:   map[string]int{},
	}
	edgeCount := 0
	for _, child := range events {
		if child == nil || child.EventID == "" || child.ParentSpanID == "" {
			continue
		}
		parent := spanOwner[child.ParentSpanID]
		if parent == nil || parent.EventID == child.EventID {
			return g
		}
		g.children[parent.EventID] = append(g.children[parent.EventID], child)
		g.parents[child.EventID] = parent.EventID
		edgeCount++
	}
	if edgeCount == 0 {
		return g
	}

	var depthOf func(string, map[string]struct{}) int
	depthOf = func(eventID string, stack map[string]struct{}) int {
		if d, ok := g.depths[eventID]; ok {
			return d
		}
		parentID := g.parents[eventID]
		if parentID == "" {
			g.depths[eventID] = 0
			return 0
		}
		if _, cyclic := stack[eventID]; cyclic {
			return 0
		}
		stack[eventID] = struct{}{}
		d := depthOf(parentID, stack) + 1
		delete(stack, eventID)
		g.depths[eventID] = d
		return d
	}
	for eventID := range byEventID {
		depthOf(eventID, map[string]struct{}{})
	}
	g.usable = true
	return g
}

func orderByTimestamp(events []*eventv2.Event) []*eventv2.Event {
	out := append([]*eventv2.Event(nil), events...)
	sortEventsStable(out)
	return out
}

func sortEventsStable(events []*eventv2.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		return compareEventIdentity(events[i], events[j]) < 0
	})
}

func compareEventIdentity(a, b *eventv2.Event) int {
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
		if a.TsStart.Before(b.TsStart) {
			return -1
		}
		return 1
	}
	if a.Service != b.Service {
		if a.Service < b.Service {
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
