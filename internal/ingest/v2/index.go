package ingestv2

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type ErrorKey struct {
	Service   string
	Step      string
	ErrorCode string
}

type CallKey struct {
	From     string
	To       string
	Endpoint string
}

type serviceNode struct {
	Name      string
	FirstSeen time.Time
	LastSeen  time.Time
}

type errorNode struct {
	Key       ErrorKey
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

type callEdge struct {
	Key       CallKey
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

type IndexSizes struct {
	Events   int
	Traces   int
	Services int
	Errors   int
	Calls    int
}

type PruneResult struct {
	Events int
}

type RecentIndex struct {
	mu       sync.RWMutex
	byID     map[string]*eventv2.Event
	byTrace  map[string][]*eventv2.Event
	services map[string]*serviceNode
	errors   map[ErrorKey]*errorNode
	calls    map[CallKey]*callEdge
	size     *prometheus.GaugeVec
}

func NewRecentIndex(sizeGauge *prometheus.GaugeVec) *RecentIndex {
	idx := &RecentIndex{
		byID:     map[string]*eventv2.Event{},
		byTrace:  map[string][]*eventv2.Event{},
		services: map[string]*serviceNode{},
		errors:   map[ErrorKey]*errorNode{},
		calls:    map[CallKey]*callEdge{},
		size:     sizeGauge,
	}
	idx.observeLocked()
	return idx
}

func (idx *RecentIndex) Insert(ev *eventv2.Event) bool {
	if idx == nil || ev == nil || ev.EventID == "" {
		return false
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.byID[ev.EventID]; ok {
		return false
	}
	cp := cloneEvent(ev)
	idx.byID[cp.EventID] = cp
	idx.byTrace[cp.TraceID] = append(idx.byTrace[cp.TraceID], cp)
	idx.applyAggregatesLocked(cp)
	idx.observeLocked()
	return true
}

func (idx *RecentIndex) GetByID(eventID string) (*eventv2.Event, bool) {
	if idx == nil || eventID == "" {
		return nil, false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ev, ok := idx.byID[eventID]
	if !ok {
		return nil, false
	}
	return cloneEvent(ev), true
}

func (idx *RecentIndex) TraceEvents(traceID string) []*eventv2.Event {
	if idx == nil || traceID == "" {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	events := idx.byTrace[traceID]
	out := make([]*eventv2.Event, 0, len(events))
	for _, ev := range events {
		out = append(out, cloneEvent(ev))
	}
	return out
}

func (idx *RecentIndex) SnapshotEvents() []*eventv2.Event {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]*eventv2.Event, 0, len(idx.byID))
	for _, ev := range idx.byID {
		out = append(out, ev)
	}
	return out
}

func (idx *RecentIndex) SnapshotTrace(traceID string) []*eventv2.Event {
	if idx == nil || traceID == "" {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return append([]*eventv2.Event(nil), idx.byTrace[traceID]...)
}

func (idx *RecentIndex) SnapshotTraces() map[string][]*eventv2.Event {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string][]*eventv2.Event, len(idx.byTrace))
	for traceID, events := range idx.byTrace {
		out[traceID] = append([]*eventv2.Event(nil), events...)
	}
	return out
}

func (idx *RecentIndex) Sizes() IndexSizes {
	if idx == nil {
		return IndexSizes{}
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.sizesLocked()
}

func (idx *RecentIndex) PruneOlderThan(cutoff time.Time) PruneResult {
	if idx == nil || cutoff.IsZero() {
		return PruneResult{}
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	pruned := 0
	keptByTrace := make(map[string][]*eventv2.Event, len(idx.byTrace))
	for traceID, events := range idx.byTrace {
		for _, ev := range events {
			if ev.TsEnd.Before(cutoff) {
				pruned++
				continue
			}
			keptByTrace[traceID] = append(keptByTrace[traceID], ev)
		}
	}
	if pruned == 0 {
		idx.observeLocked()
		return PruneResult{}
	}

	idx.byID = map[string]*eventv2.Event{}
	idx.byTrace = map[string][]*eventv2.Event{}
	idx.services = map[string]*serviceNode{}
	idx.errors = map[ErrorKey]*errorNode{}
	idx.calls = map[CallKey]*callEdge{}
	for traceID, events := range keptByTrace {
		if len(events) == 0 {
			continue
		}
		idx.byTrace[traceID] = events
		for _, ev := range events {
			idx.byID[ev.EventID] = ev
			idx.applyAggregatesLocked(ev)
		}
	}
	idx.observeLocked()
	return PruneResult{Events: pruned}
}

func (idx *RecentIndex) applyAggregatesLocked(ev *eventv2.Event) {
	ts := ev.TsEnd
	if ts.IsZero() {
		ts = ev.TsStart
	}
	idx.touchServiceLocked(ev.Service, ts)

	if ev.Status == eventv2.StatusSuppressed {
		return
	}

	if isFailedStatus(ev.Status) && ev.Anchor != nil {
		key := ErrorKey{Service: ev.Service, Step: ev.Anchor.Step, ErrorCode: ev.Anchor.ErrorCode}
		node := idx.errors[key]
		if node == nil {
			node = &errorNode{Key: key, FirstSeen: ts}
			idx.errors[key] = node
		}
		node.Count++
		touchRange(&node.FirstSeen, &node.LastSeen, ts)
	}

	for _, step := range ev.Steps {
		if step.Downstream == nil || step.Downstream.Service == "" {
			continue
		}
		key := CallKey{From: ev.Service, To: step.Downstream.Service, Endpoint: step.Downstream.Endpoint}
		edge := idx.calls[key]
		if edge == nil {
			edge = &callEdge{Key: key, FirstSeen: ts}
			idx.calls[key] = edge
		}
		edge.Count++
		touchRange(&edge.FirstSeen, &edge.LastSeen, ts)
	}
}

func (idx *RecentIndex) touchServiceLocked(service string, ts time.Time) {
	if service == "" {
		return
	}
	node := idx.services[service]
	if node == nil {
		node = &serviceNode{Name: service, FirstSeen: ts}
		idx.services[service] = node
	}
	touchRange(&node.FirstSeen, &node.LastSeen, ts)
}

func (idx *RecentIndex) sizesLocked() IndexSizes {
	return IndexSizes{
		Events:   len(idx.byID),
		Traces:   len(idx.byTrace),
		Services: len(idx.services),
		Errors:   len(idx.errors),
		Calls:    len(idx.calls),
	}
}

func (idx *RecentIndex) observeLocked() {
	if idx.size == nil {
		return
	}
	sizes := idx.sizesLocked()
	idx.size.WithLabelValues("event").Set(float64(sizes.Events))
	idx.size.WithLabelValues("trace").Set(float64(sizes.Traces))
	idx.size.WithLabelValues("service").Set(float64(sizes.Services))
	idx.size.WithLabelValues("error").Set(float64(sizes.Errors))
	idx.size.WithLabelValues("call").Set(float64(sizes.Calls))
}

func touchRange(first, last *time.Time, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if first.IsZero() || ts.Before(*first) {
		*first = ts
	}
	if last.IsZero() || ts.After(*last) {
		*last = ts
	}
}

func isFailedStatus(status eventv2.Status) bool {
	switch status {
	case eventv2.StatusError, eventv2.StatusTimeout, eventv2.StatusPartial, eventv2.StatusAborted:
		return true
	default:
		return false
	}
}

func cloneEvent(ev *eventv2.Event) *eventv2.Event {
	if ev == nil {
		return nil
	}
	cp := *ev
	if ev.Anchor != nil {
		anchor := *ev.Anchor
		cp.Anchor = &anchor
	}
	cp.Steps = append([]eventv2.Step(nil), ev.Steps...)
	for i := range cp.Steps {
		if ev.Steps[i].Downstream != nil {
			downstream := *ev.Steps[i].Downstream
			cp.Steps[i].Downstream = &downstream
		}
		if ev.Steps[i].Error != nil {
			stepErr := *ev.Steps[i].Error
			cp.Steps[i].Error = &stepErr
		}
	}
	cp.Logs = append([]eventv2.Log(nil), ev.Logs...)
	for i := range cp.Logs {
		cp.Logs[i].Fields = cloneMap(ev.Logs[i].Fields)
	}
	cp.Errors = append([]eventv2.ErrorRef(nil), ev.Errors...)
	cp.Fields = cloneMap(ev.Fields)
	return &cp
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue handles value shapes produced by json.Unmarshal. Maps/slices are
// copied recursively; primitive values are immutable and can be reused.
func cloneValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneValue(typed[i])
		}
		return out
	default:
		return v
	}
}
