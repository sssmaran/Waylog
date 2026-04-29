package ingestv2

import (
	"sort"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type AnchorResult struct {
	Event   *eventv2.Event
	Linkage string
}

type ResolveOpts struct {
	ExcludeSuppressed bool
}

func ResolveAnchor(events []*eventv2.Event) AnchorResult {
	return ResolveAnchorWithOptions(events, ResolveOpts{})
}

func ResolveAnchorWithOptions(events []*eventv2.Event, opts ResolveOpts) AnchorResult {
	if len(events) == 0 {
		return AnchorResult{Linkage: LinkageTimestampFallback}
	}

	failed := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		if anchorCandidate(ev, opts) && ev.Status.IsFailed() {
			failed = append(failed, ev)
		}
	}
	if len(failed) == 0 {
		ev, linkage := selectTraceRoot(events, opts)
		return AnchorResult{Event: ev, Linkage: linkage}
	}

	graph := buildTraceGraph(events)
	if graph.usable && graphTouchesFailed(graph, failed) {
		leaf := deepestFailedLeaf(graph, failed)
		if leaf != nil {
			return AnchorResult{Event: leaf, Linkage: LinkageCausal}
		}
	}

	sortEventsStable(failed)
	return AnchorResult{Event: failed[0], Linkage: LinkageTimestampFallback}
}

func selectTraceRoot(events []*eventv2.Event, opts ResolveOpts) (*eventv2.Event, string) {
	candidates := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		if anchorCandidate(ev, opts) && ev.ParentSpanID == "" {
			candidates = append(candidates, ev)
		}
	}
	linkage := LinkageTimestampFallback
	if len(candidates) == 0 {
		graph := buildTraceGraph(events)
		for _, ev := range events {
			if anchorCandidate(ev, opts) && ev.ParentSpanID != "" && graph.parents[ev.EventID] == "" {
				candidates = append(candidates, ev)
			}
		}
		if graph.usable {
			linkage = LinkageCausal
		}
	}
	if len(candidates) == 0 {
		for _, ev := range events {
			if anchorCandidate(ev, opts) {
				candidates = append(candidates, ev)
			}
		}
	}
	sortEventsStable(candidates)
	if len(candidates) == 0 {
		return nil, linkage
	}
	return candidates[0], linkage
}

func anchorCandidate(ev *eventv2.Event, opts ResolveOpts) bool {
	if ev == nil {
		return false
	}
	return !opts.ExcludeSuppressed || ev.Status != eventv2.StatusSuppressed
}

func graphTouchesFailed(graph traceGraph, failed []*eventv2.Event) bool {
	for _, ev := range failed {
		if ev == nil {
			continue
		}
		if graph.parents[ev.EventID] != "" || len(graph.children[ev.EventID]) > 0 {
			return true
		}
	}
	return false
}

func deepestFailedLeaf(graph traceGraph, failed []*eventv2.Event) *eventv2.Event {
	failedIDs := map[string]struct{}{}
	for _, ev := range failed {
		if ev != nil {
			failedIDs[ev.EventID] = struct{}{}
		}
	}
	leaves := make([]*eventv2.Event, 0, len(failed))
	for _, ev := range failed {
		if ev == nil || hasFailedDescendant(graph, ev.EventID, failedIDs, map[string]struct{}{}) {
			continue
		}
		leaves = append(leaves, ev)
	}
	if len(leaves) == 0 {
		return nil
	}
	sort.SliceStable(leaves, func(i, j int) bool {
		di := graph.depths[leaves[i].EventID]
		dj := graph.depths[leaves[j].EventID]
		if di != dj {
			return di > dj
		}
		return compareEventIdentity(leaves[i], leaves[j]) < 0
	})
	return leaves[0]
}

func hasFailedDescendant(graph traceGraph, eventID string, failed map[string]struct{}, seen map[string]struct{}) bool {
	if _, ok := seen[eventID]; ok {
		return false
	}
	seen[eventID] = struct{}{}
	for _, child := range graph.children[eventID] {
		if child == nil {
			continue
		}
		if _, ok := failed[child.EventID]; ok {
			return true
		}
		if hasFailedDescendant(graph, child.EventID, failed, seen) {
			return true
		}
	}
	return false
}
