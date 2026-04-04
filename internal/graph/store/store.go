package store

import (
	"sort"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/window"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

type Store struct {
	mu    sync.RWMutex
	graph *core.Graph
	//for fast lookups
	requestFacts    map[string]RequestFacts
	seenRequests    map[string]struct{}
	counters        *Counters
	serviceStats    map[string]*ServiceStats
	edgeSet         map[string]struct{}            // "from:to:type" for dedup
	traceToRequest  map[string]string              // trace_id -> request node ID
	traceToSpans    map[string][]string            // trace_id -> []span node IDs
	errorIndex      map[string]map[string]struct{} // error_code -> set of request IDs
	errorIndexReady bool
}

func NewStore() *Store {
	return &Store{
		graph:           core.New(),
		requestFacts:    map[string]RequestFacts{},
		seenRequests:    map[string]struct{}{},
		counters:        NewCounters(),
		serviceStats:    map[string]*ServiceStats{},
		edgeSet:         map[string]struct{}{},
		traceToRequest:  map[string]string{},
		traceToSpans:    map[string][]string{},
		errorIndex:      map[string]map[string]struct{}{},
		errorIndexReady: true,
	}
}

// ensureGraphLocked guarantees s.graph is non-nil.
// Call ONLY while holding s.mu.
func (s *Store) ensureGraphLocked() {
	if s.graph == nil {
		s.graph = core.New()
	}
}

// Merge merges another graph into this store.
// Node IDs are deterministic, so duplicates are avoided.
// Edges are append-only.
func (s *Store) Merge(g *core.Graph) {
	if g == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGraphLocked()

	// Merge nodes
	for id, incoming := range g.Nodes {
		existing, exists := s.graph.Nodes[id]
		if !exists {
			s.graph.Nodes[id] = incoming
			continue
		}

		// Merge time ranges (imp!)
		mergeNodeTime(&existing, &incoming)

		// Deterministic merge for request and legacy span nodes.
		if existing.Type == core.NodeRequest {
			mergeRequestAttrs(&existing, &incoming)
		}
		if existing.Type == core.NodeSpan {
			mergeSpanAttrs(&existing, &incoming)
		}
		s.graph.Nodes[id] = existing
	}
	// Merge edges (deduplicated); collect FailedWith edges for deferred error index update
	var failedEdges []core.Edge
	for _, e := range g.Edges {
		key := e.From + ":" + e.To + ":" + string(e.Type)
		if _, exists := s.edgeSet[key]; exists {
			continue
		}
		s.edgeSet[key] = struct{}{}
		s.graph.Edges = append(s.graph.Edges, e)
		if s.graph.OutEdges == nil {
			s.graph.OutEdges = make(map[string][]core.Edge)
		}
		if s.graph.InEdges == nil {
			s.graph.InEdges = make(map[string][]core.Edge)
		}
		s.graph.OutEdges[e.From] = append(s.graph.OutEdges[e.From], e)
		s.graph.InEdges[e.To] = append(s.graph.InEdges[e.To], e)

		if e.Type == core.EdgeFailedWith {
			failedEdges = append(failedEdges, e)
		}
	}

	// Update trace indexes
	for id, n := range g.Nodes {
		traceID, _ := n.Attr["trace_id"].(string)
		if traceID == "" {
			continue
		}
		switch n.Type {
		case core.NodeRequest:
			s.traceToRequest[traceID] = id
		case core.NodeSpan:
			s.traceToSpans[traceID] = appendUniqueString(s.traceToSpans[traceID], id)
		}
	}

	for id, n := range g.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}

		// Always extract from the merged graph (not the delta)
		facts, ok := extractRequestFactsFromGraph(s.graph, id)
		if !ok {
			continue
		}

		if _, seen := s.seenRequests[id]; seen {
			oldFacts := s.requestFacts[id]
			if !factsEqual(oldFacts, facts) {
				s.reverseFactsFromCountersLocked(oldFacts)
				s.applyFactsToCountersLocked(facts)
			}
			s.requestFacts[id] = facts
			continue
		}

		s.seenRequests[id] = struct{}{}
		s.requestFacts[id] = facts
		s.applyFactsToCountersLocked(facts)
	}

	// Process error index with fully-populated traceToRequest
	for _, e := range failedEdges {
		s.addToErrorIndexLocked(e)
	}
}

func (s *Store) applyFactsToCountersLocked(f RequestFacts) {
	// error counts
	for _, errID := range f.Errors {
		s.counters.ErrorCount[errID]++
		// service -> error
		for _, svcID := range f.Services {
			m := s.counters.ServiceErrorCount[svcID]
			if m == nil {
				m = map[string]int{}
				s.counters.ServiceErrorCount[svcID] = m
			}
			m[errID]++
		}
		// flag -> error
		for _, flagID := range f.FeatureFlags {
			m := s.counters.FlagErrorCount[flagID]
			if m == nil {
				m = map[string]int{}
				s.counters.FlagErrorCount[flagID] = m
			}
			m[errID]++
		}
	}

	services := f.Services
	if len(services) == 0 && f.RootService != "" {
		services = []string{f.RootService}
	}
	for _, svcID := range services {
		stats := s.serviceStats[svcID]
		if stats == nil {
			stats = &ServiceStats{}
			s.serviceStats[svcID] = stats
		}
		stats.Invocations++
		if len(f.Errors) > 0 {
			stats.Errors++
		}
	}
}

func (s *Store) reverseFactsFromCountersLocked(f RequestFacts) {
	for _, errID := range f.Errors {
		s.counters.ErrorCount[errID]--
		if s.counters.ErrorCount[errID] <= 0 {
			delete(s.counters.ErrorCount, errID)
		}
		for _, svcID := range f.Services {
			m := s.counters.ServiceErrorCount[svcID]
			if m != nil {
				m[errID]--
				if m[errID] <= 0 {
					delete(m, errID)
				}
				if len(m) == 0 {
					delete(s.counters.ServiceErrorCount, svcID)
				}
			}
		}
		for _, flagID := range f.FeatureFlags {
			m := s.counters.FlagErrorCount[flagID]
			if m != nil {
				m[errID]--
				if m[errID] <= 0 {
					delete(m, errID)
				}
				if len(m) == 0 {
					delete(s.counters.FlagErrorCount, flagID)
				}
			}
		}
	}

	services := f.Services
	if len(services) == 0 && f.RootService != "" {
		services = []string{f.RootService}
	}
	for _, svcID := range services {
		stats := s.serviceStats[svcID]
		if stats == nil {
			continue
		}
		stats.Invocations--
		if len(f.Errors) > 0 {
			stats.Errors--
		}
		if stats.Invocations <= 0 && stats.Errors <= 0 {
			delete(s.serviceStats, svcID)
		}
	}
}

func factsEqual(a, b RequestFacts) bool {
	return sortedEqual(a.Services, b.Services) &&
		sortedEqual(a.Errors, b.Errors) &&
		sortedEqual(a.FeatureFlags, b.FeatureFlags)
}

func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := make([]string, len(a))
	copy(ac, a)
	bc := make([]string, len(b))
	copy(bc, b)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

//helper for time-window commands
// internal/graph/store.go

func mergeNodeTime(dst, src *core.Node) {
	if !src.FirstSeen.IsZero() &&
		(dst.FirstSeen.IsZero() || src.FirstSeen.Before(dst.FirstSeen)) {
		dst.FirstSeen = src.FirstSeen
	}

	if !src.LastSeen.IsZero() &&
		(dst.LastSeen.IsZero() || src.LastSeen.After(dst.LastSeen)) {
		dst.LastSeen = src.LastSeen
	}
}

// mergeRequestAttrs applies deterministic merge rules for request nodes.
// - success: AND (any failure makes the request failed)
// - If incoming is from root span (is_root=true): overwrite status_code, latency_ms, event_name, flow, root_service
// - error_codes: accumulated as deduplicated []string
func mergeRequestAttrs(dst, src *core.Node) {
	if dst.Attr == nil {
		dst.Attr = map[string]any{}
	}
	if src.Attr == nil {
		return
	}

	// success = AND: any false makes it false
	if srcSuccess, ok := src.Attr["success"].(bool); ok && !srcSuccess {
		dst.Attr["success"] = false
	}

	mergeRequestScalar(dst.Attr, src.Attr, "version")
	mergeRequestScalar(dst.Attr, src.Attr, "user_id")
	mergeRequestScalar(dst.Attr, src.Attr, "user_tier")
	mergeRequestScalar(dst.Attr, src.Attr, "user_region")
	if _, ok := dst.Attr["user_vip"]; !ok {
		if v, ok := src.Attr["user_vip"]; ok {
			dst.Attr["user_vip"] = v
		}
	}
	mergeRequestStringSlice(dst.Attr, src.Attr, "feature_flags")

	// If incoming event is from root span, its values become the trace-level summary
	if isRoot, ok := src.Attr["is_root"].(bool); ok && isRoot {
		if v, ok := src.Attr["status_code"]; ok {
			dst.Attr["status_code"] = v
		}
		if v, ok := src.Attr["latency_ms"]; ok {
			dst.Attr["latency_ms"] = v
		}
		if v, ok := src.Attr["event_name"]; ok {
			dst.Attr["event_name"] = v
		}
		if v, ok := src.Attr["flow"]; ok {
			dst.Attr["flow"] = v
		}
		if v, ok := src.Attr["service"]; ok {
			dst.Attr["root_service"] = v
		}
		if v, ok := src.Attr["http_method"].(string); ok && v != "" {
			dst.Attr["http_method"] = v
		}
		if v, ok := src.Attr["route_template"].(string); ok && v != "" {
			dst.Attr["route_template"] = v
		}
		dst.Attr["is_root"] = true
	}

	// Accumulate error_codes as deduplicated []string
	mergeErrorCodes(dst, src)
}

func mergeRequestScalar(dst, src map[string]any, key string) {
	if _, ok := dst[key]; ok {
		return
	}
	if v, ok := src[key]; ok {
		dst[key] = v
	}
}

func mergeRequestStringSlice(dst, src map[string]any, key string) {
	values := append(AttrToStringSlice(dst[key]), AttrToStringSlice(src[key])...)
	if len(values) == 0 {
		return
	}
	dst[key] = dedupeStringSlice(values)
}

func dedupeStringSlice(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func mergeErrorCodes(dst, src *core.Node) {
	var codes []string
	seen := map[string]struct{}{}
	appendCode := func(code string) {
		if code == "" {
			return
		}
		if _, exists := seen[code]; exists {
			return
		}
		codes = append(codes, code)
		seen[code] = struct{}{}
	}

	// Include prior merged state first, then single-code attrs, then incoming values.
	for _, c := range AttrToStringSlice(dst.Attr["error_codes"]) {
		appendCode(c)
	}
	if dstErr, ok := dst.Attr["error_code"].(string); ok {
		appendCode(dstErr)
	}
	for _, c := range AttrToStringSlice(src.Attr["error_codes"]) {
		appendCode(c)
	}
	if srcErr, ok := src.Attr["error_code"].(string); ok {
		appendCode(srcErr)
	}

	if len(codes) > 0 {
		dst.Attr["error_codes"] = codes
	}
}

// AttrToStringSlice extracts a []string from an attribute value that may be
// typed as []string or []any (the latter occurs after JSON round-trip).
func AttrToStringSlice(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// mergeSpanAttrs enriches a stub span node with data from the real event.
// Stubs are created when a child event arrives before its parent's own event.
// When the parent's event arrives, its enriched fields fill in the gaps.
func mergeSpanAttrs(dst, src *core.Node) {
	if dst.Attr == nil {
		dst.Attr = map[string]any{}
	}
	if src.Attr == nil {
		return
	}

	// Fill in any attrs that dst is missing from src
	enrichKeys := []string{
		"event_name", "status_code", "success", "latency_ms",
		"flow", "timestamp", "caller_service", "downstream_service",
		"service", "error_code", "http_method", "route_template",
	}
	for _, key := range enrichKeys {
		if _, hasDst := dst.Attr[key]; !hasDst {
			if v, hasSrc := src.Attr[key]; hasSrc {
				dst.Attr[key] = v
			}
		}
	}
}

// Graph returns the live graph pointer.
// IMPORTANT: callers MUST treat this as read-only.
func (s *Store) Graph() *core.Graph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureGraphLocked()

	return s.graph
}

// Snapshot returns a deep copy of the graph.
// This is safe for persistence, debugging, and CLI reads.
func (s *Store) Snapshot() *core.Graph {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// imp! graph must never be nil
	s.ensureGraphLocked()

	// Deep copy nodes
	nodes := make(map[string]core.Node, len(s.graph.Nodes))
	for id, n := range s.graph.Nodes {
		var attr map[string]any
		if n.Attr != nil {
			attr = make(map[string]any, len(n.Attr))
			for k, v := range n.Attr {
				attr[k] = v
			}
		}

		nodes[id] = core.Node{
			ID:        n.ID,
			Type:      n.Type,
			Attr:      attr,
			FirstSeen: n.FirstSeen,
			LastSeen:  n.LastSeen,
		}
	}

	// Copy edges
	edges := make([]core.Edge, len(s.graph.Edges))
	copy(edges, s.graph.Edges)

	snap := &core.Graph{
		Nodes: nodes,
		Edges: edges,
	}
	snap.RebuildIndexes()
	return snap
}

// RequestIDForTrace returns the request node ID for a given trace ID.
func (s *Store) RequestIDForTrace(traceID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.traceToRequest[traceID]
	return id, ok
}

// SpanIDsForTrace returns all span node IDs for a given trace ID.
func (s *Store) SpanIDsForTrace(traceID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spanIDs := s.traceToSpans[traceID]
	return append([]string(nil), spanIDs...)
}

// Restore replaces the current graph with a defensive copy of g.
// This avoids memory aliasing with snapshot data.
func (s *Store) Restore(g *core.Graph) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if g == nil {
		s.graph = core.New()
		s.rebuildDerivedIndexesLocked()
		return
	}

	// Deep-copy nodes
	nodes := make(map[string]core.Node, len(g.Nodes))
	for id, n := range g.Nodes {
		var attrCopy map[string]any
		if n.Attr != nil {
			attrCopy = make(map[string]any, len(n.Attr))
			for k, v := range n.Attr {
				attrCopy[k] = v
			}
		}

		nodes[id] = core.Node{
			ID:        n.ID,
			Type:      n.Type,
			Attr:      attrCopy,
			FirstSeen: n.FirstSeen,
			LastSeen:  n.LastSeen,
		}

	}

	// Copy edges
	edges := make([]core.Edge, len(g.Edges))
	copy(edges, g.Edges)

	s.graph = &core.Graph{
		Nodes: nodes,
		Edges: edges,
	}
	s.backfillRequestTimestampsLocked()
	s.rebuildDerivedIndexesLocked()
}

// PruneOlderThan drops requests with LastSeen before cutoff.
// This keeps 1-hop context for remaining requests.
func (s *Store) PruneOlderThan(cutoff time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureGraphLocked()
	s.graph = window.FilterByWindow(s.graph, cutoff, time.Now())
	s.rebuildDerivedIndexesLocked()
}

func (s *Store) rebuildDerivedIndexesLocked() {
	s.requestFacts = map[string]RequestFacts{}
	s.seenRequests = map[string]struct{}{}
	s.counters = NewCounters()
	s.serviceStats = map[string]*ServiceStats{}
	s.edgeSet = map[string]struct{}{}
	s.traceToRequest = map[string]string{}
	s.traceToSpans = map[string][]string{}
	s.errorIndex = map[string]map[string]struct{}{}
	s.errorIndexReady = false

	// Rebuild edge set; collect FailedWith for deferred error index update
	var failedEdges []core.Edge
	for _, e := range s.graph.Edges {
		key := e.From + ":" + e.To + ":" + string(e.Type)
		s.edgeSet[key] = struct{}{}
		if e.Type == core.EdgeFailedWith {
			failedEdges = append(failedEdges, e)
		}
	}
	s.graph.RebuildIndexes()

	// Rebuild trace indexes (must precede error index for span→request lookup)
	for id, n := range s.graph.Nodes {
		traceID, _ := n.Attr["trace_id"].(string)
		if traceID == "" {
			continue
		}
		switch n.Type {
		case core.NodeRequest:
			s.traceToRequest[traceID] = id
		case core.NodeSpan:
			s.traceToSpans[traceID] = appendUniqueString(s.traceToSpans[traceID], id)
		}
	}

	// Now build error index with fully-populated traceToRequest
	for _, e := range failedEdges {
		s.addToErrorIndexLocked(e)
	}
	s.errorIndexReady = true

	for id, n := range s.graph.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		facts, ok := extractRequestFactsFromGraph(s.graph, id)
		if !ok {
			continue
		}
		s.seenRequests[id] = struct{}{}
		s.requestFacts[id] = facts
		s.applyFactsToCountersLocked(facts)
	}
}

func (s *Store) backfillRequestTimestampsLocked() {
	for id, n := range s.graph.Nodes {
		if n.Type != core.NodeRequest {
			continue
		}
		if !n.LastSeen.IsZero() {
			continue
		}
		ts := parseTimestampAttr(n.Attr)
		if ts.IsZero() {
			continue
		}
		n.FirstSeen = ts
		n.LastSeen = ts
		s.graph.Nodes[id] = n
	}
}

func parseTimestampAttr(attr map[string]any) time.Time {
	if attr == nil {
		return time.Time{}
	}
	if v, ok := attr["timestamp"]; ok {
		switch t := v.(type) {
		case time.Time:
			return t
		case string:
			if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return ts
			}
			if ts, err := time.Parse(time.RFC3339, t); err == nil {
				return ts
			}
		case float64:
			return time.Unix(int64(t), 0).UTC()
		case int64:
			return time.Unix(t, 0).UTC()
		case int:
			return time.Unix(int64(t), 0).UTC()
		}
	}
	return time.Time{}
}

func appendUniqueString(values []string, candidate string) []string {
	for _, existing := range values {
		if existing == candidate {
			return values
		}
	}
	return append(values, candidate)
}

// addToErrorIndexLocked adds a FailedWith edge to the error index.
// Must be called with s.mu held.
func (s *Store) addToErrorIndexLocked(e core.Edge) {
	errNode, ok := s.graph.Nodes[e.To]
	if !ok {
		return
	}
	code, _ := errNode.Attr["code"].(string)
	if code == "" {
		return
	}

	// Determine request ID: edge.From is either a request or span node
	reqID := ""
	fromNode, ok := s.graph.Nodes[e.From]
	if !ok {
		return
	}
	switch fromNode.Type {
	case core.NodeRequest:
		reqID = e.From
	case core.NodeSpan:
		// Look up via traceToRequest
		traceID, _ := fromNode.Attr["trace_id"].(string)
		if traceID != "" {
			reqID = s.traceToRequest[traceID]
		}
	}
	if reqID == "" {
		return
	}

	set := s.errorIndex[code]
	if set == nil {
		set = map[string]struct{}{}
		s.errorIndex[code] = set
	}
	set[reqID] = struct{}{}
}

// ErrorIndex returns request IDs affected by a given error code.
// The bool indicates whether the index is ready (valid).
func (s *Store) ErrorIndex(errorCode string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.errorIndexReady {
		return nil, false
	}
	set := s.errorIndex[errorCode]
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out, true
}

func extractRequestFactsFromGraph(g *core.Graph, reqID string) (RequestFacts, bool) {
	reqNode, ok := g.Nodes[reqID]
	if !ok || reqNode.Type != core.NodeRequest {
		return RequestFacts{}, false
	}

	f := RequestFacts{
		RequestID: reqID,
		SeenAt:    reqNode.LastSeen,
	}

	// Extract attrs from request node when available. This supports the
	// flattened graph shape while remaining compatible with legacy snapshots.
	if reqNode.Attr != nil {
		if rs, ok := reqNode.Attr["root_service"].(string); ok {
			f.RootService = rs
		}
		switch v := reqNode.Attr["latency_ms"].(type) {
		case int64:
			f.LatencyMs = v
		case int:
			f.LatencyMs = int64(v)
		case float64:
			f.LatencyMs = int64(v)
		}
		if st, ok := reqNode.Attr["status"].(string); ok {
			f.Status = st
		} else if success, ok := reqNode.Attr["success"].(bool); ok {
			if success {
				f.Status = "success"
			} else {
				f.Status = "failure"
			}
		}
		if version, ok := reqNode.Attr["version"].(string); ok {
			f.Version = version
		}
		if uid, ok := reqNode.Attr["user_id"].(string); ok {
			f.UserID = uid
		}
		if tier, ok := reqNode.Attr["user_tier"].(string); ok {
			f.UserTier = tier
		}
		if vip, ok := reqNode.Attr["user_vip"].(bool); ok {
			f.UserVIP = vip
		}
		if region, ok := reqNode.Attr["user_region"].(string); ok {
			f.UserRegion = region
		}
		if flags := AttrToStringSlice(reqNode.Attr["feature_flags"]); len(flags) > 0 {
			f.FeatureFlags = append([]string(nil), flags...)
		}
		if len(f.FeatureFlags) == 0 {
			if legacyFlags := AttrToStringSlice(reqNode.Attr["flags"]); len(legacyFlags) > 0 {
				f.FeatureFlags = append([]string(nil), legacyFlags...)
			}
		}
	}

	// Gather neighbors via adjacency indexes.
	// Store human-readable names/codes (not node IDs) so queries like
	// error_code=PMT_502 or service=checkout work directly.
	for _, e := range g.OutEdges[reqID] {
		if n, ok := g.Nodes[e.To]; ok {
			switch n.Type {
			case core.NodeService:
				if name, _ := n.Attr["name"].(string); name != "" {
					f.Services = append(f.Services, name)
				} else {
					f.Services = append(f.Services, n.ID)
				}
			case core.NodeError:
				if code, _ := n.Attr["code"].(string); code != "" {
					f.Errors = append(f.Errors, code)
				} else {
					f.Errors = append(f.Errors, n.ID)
				}
			case core.NodeFlag:
				if name, _ := n.Attr["name"].(string); name != "" {
					f.FeatureFlags = append(f.FeatureFlags, name)
				} else {
					f.FeatureFlags = append(f.FeatureFlags, n.ID)
				}
			}
		}
	}
	for _, e := range g.InEdges[reqID] {
		if n, ok := g.Nodes[e.From]; ok {
			switch n.Type {
			case core.NodeService:
				if name, _ := n.Attr["name"].(string); name != "" {
					f.Services = append(f.Services, name)
				} else {
					f.Services = append(f.Services, n.ID)
				}
			case core.NodeError:
				if code, _ := n.Attr["code"].(string); code != "" {
					f.Errors = append(f.Errors, code)
				} else {
					f.Errors = append(f.Errors, n.ID)
				}
			case core.NodeFlag:
				if name, _ := n.Attr["name"].(string); name != "" {
					f.FeatureFlags = append(f.FeatureFlags, name)
				} else {
					f.FeatureFlags = append(f.FeatureFlags, n.ID)
				}
			}
		}
	}

	return f, true
}

// TraceStore returns nil. The graph store does not own a trace store; callers
// that need one should inject it via a wrapper (e.g. frozenStore in the ingest
// handler). This method exists so *Store satisfies the tools.Store interface.
func (s *Store) TraceStore() *tracestore.Store { return nil }

func (s *Store) ServiceStats() map[string]ServiceStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]ServiceStats, len(s.serviceStats))
	for name, stats := range s.serviceStats {
		if stats == nil {
			continue
		}
		out[name] = *stats
	}
	return out
}
