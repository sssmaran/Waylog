package detect

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/graph/analysis"
	"github.com/sssmaran/WaylogCLI/internal/graph/causal"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

// DeploySource provides recent deployments for correlation.
// Satisfied by *coldstore.SQLiteStore.
type DeploySource interface {
	DeploymentsInWindow(ctx context.Context, start, end time.Time, serviceFilter string) ([]coldstore.Deployment, error)
}

// Detector runs a periodic loop that compares error rates across
// two time windows and surfaces structured insights when a spike is detected.
type Detector struct {
	cfg     Config
	store   *store.Store
	traces  *tracestore.Store // nil OK — falls back to graph topology
	deploys DeploySource      // nil if cold store unavailable
	current atomic.Pointer[Insight]
}

func NewDetector(cfg Config, s *store.Store, ts *tracestore.Store, deploys DeploySource) *Detector {
	return &Detector{
		cfg:     cfg,
		store:   s,
		traces:  ts,
		deploys: deploys,
	}
}

// Current returns the active insight, or nil if no spike is detected.
func (d *Detector) Current() *Insight {
	return d.current.Load()
}

// Run blocks until ctx is cancelled, ticking at cfg.Interval.
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	slog.Info("anomaly detector started", "interval", d.cfg.Interval,
		"current_window", d.cfg.CurrentWindow, "baseline_window", d.cfg.BaselineWindow)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Detector) tick(ctx context.Context) {
	now := time.Now().UTC()
	snap := d.store.Snapshot()

	// Current window: [now - current, now)
	currStart := now.Add(-d.cfg.CurrentWindow)
	currRollup := analysis.RollupWindow(snap, d.store, d.traces, currStart, now)

	// Baseline window: [now - current - baseline, now - current)
	baseEnd := currStart
	baseStart := baseEnd.Add(-d.cfg.BaselineWindow)
	baseRollup := analysis.RollupWindow(snap, d.store, d.traces, baseStart, baseEnd)

	diff := analysis.DiffRollups(baseRollup, currRollup)

	// Find the top spiking error code that meets thresholds.
	topCode, topAfter, topBefore, topLift := d.findTopSpike(diff)
	if topCode == "" {
		d.current.Store(nil)
		return
	}

	// Build blast radius for the top error code.
	requests, users, vipUsers, services, severity := d.computeBlast(topCode, currStart, now)

	insight := &Insight{
		DetectedAt:       now,
		TopErrorCode:     topCode,
		Lift:             topLift,
		CurrentCount:     topAfter,
		BaselineCount:    topBefore,
		AffectedRequests: requests,
		AffectedUsers:    users,
		VIPUsers:         vipUsers,
		Services:         services,
		SeverityScore:    severity,
	}

	// Optional deploy correlation.
	if d.deploys != nil {
		if dc := d.correlateDeploy(ctx, topCode, now); dc != nil {
			insight.DeployCorrelation = dc
		}
	}

	d.current.Store(insight)
	slog.Info("anomaly detected",
		"error_code", topCode,
		"lift", topLift,
		"count", topAfter,
		"requests", requests,
		"users", users,
		"severity", severity,
	)
}

type candidate struct {
	code   string
	after  int
	before int
	lift   float64
}

// findTopSpike returns the error code with the highest count that meets
// both the minimum lift and minimum count thresholds. When counts are tied
// (common in cascading failures), it prefers the root cause error — the one
// on the leaf service in the call topology.
func (d *Detector) findTopSpike(diff analysis.WindowDiff) (code string, after, before int, lift float64) {
	var candidates []candidate

	for _, e := range diff.New {
		if e.After >= d.cfg.MinCount {
			// New errors have infinite lift; treat as meeting threshold.
			candidates = append(candidates, candidate{e.ErrorCode, e.After, 0, float64(e.After)})
		}
	}
	for _, e := range diff.Increased {
		if e.After >= d.cfg.MinCount && e.Before > 0 {
			l := float64(e.After) / float64(e.Before)
			if l >= d.cfg.MinLift {
				candidates = append(candidates, candidate{e.ErrorCode, e.After, e.Before, l})
			}
		}
	}

	if len(candidates) == 0 {
		return "", 0, 0, 0
	}

	if len(candidates) == 1 {
		top := candidates[0]
		return top.code, top.after, top.before, top.lift
	}

	// Multiple candidates — use root cause scoring to pick the leaf error.
	depths := d.errorDepths(candidates)

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].after != candidates[j].after {
			return candidates[i].after > candidates[j].after
		}
		// Tie on count: prefer deeper service (higher depth = leaf = root cause).
		return depths[candidates[i].code] > depths[candidates[j].code]
	})
	top := candidates[0]
	return top.code, top.after, top.before, top.lift
}

// errorDepths computes a depth score for each candidate error code.
// The deepest error (leaf service in a call chain) is most likely the root cause
// in cascading failures. Uses the trace store for per-span attribution when
// available, falling back to graph topology.
func (d *Detector) errorDepths(candidates []candidate) map[string]int {
	// Primary strategy: use trace store spans for direct error→service→depth mapping.
	if d.traces != nil {
		if depths := d.errorDepthsFromTraces(candidates); len(depths) > 0 {
			return depths
		}
	}
	// Fallback: use graph topology (requires error node "service" attribute).
	return d.errorDepthsFromGraph(candidates)
}

// errorDepthsFromTraces samples a trace that contains the candidate error codes
// and uses its span parent-child chain to determine depth per error code.
func (d *Detector) errorDepthsFromTraces(candidates []candidate) map[string]int {
	codes := map[string]bool{}
	for _, c := range candidates {
		codes[c.code] = true
	}

	// Find a request that has at least two candidate error codes, then
	// extract its trace_id from the graph node attributes (RequestFacts.TraceID
	// is not populated by the store).
	now := time.Now().UTC()
	start := now.Add(-d.cfg.CurrentWindow)
	var sampleRequestID string
	d.store.ForEachRequestFact(start, now, func(f store.RequestFacts) {
		if sampleRequestID != "" {
			return
		}
		matched := 0
		for _, e := range f.Errors {
			if codes[e] {
				matched++
			}
		}
		if matched >= 2 {
			sampleRequestID = f.RequestID
		}
	})
	if sampleRequestID == "" {
		return nil
	}

	// Extract trace_id from the request node's attributes.
	snap := d.store.Snapshot()
	reqNode, ok := snap.Nodes[sampleRequestID]
	if !ok {
		return nil
	}
	traceID, _ := reqNode.Attr["trace_id"].(string)
	if traceID == "" {
		return nil
	}

	rec, ok := d.traces.Get(traceID)
	if !ok || len(rec.Spans) == 0 {
		return nil
	}

	// Compute span depths via parent chain.
	spans := map[string]tracestore.SpanRecord{}
	parentOf := map[string]string{}
	for _, span := range rec.Spans {
		if span.SpanID == "" {
			continue
		}
		spans[span.SpanID] = span
		if span.ParentSpanID != "" {
			parentOf[span.SpanID] = span.ParentSpanID
		}
	}

	depthCache := map[string]int{}
	visiting := map[string]bool{}
	var depth func(string) int
	depth = func(id string) int {
		if d, ok := depthCache[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		pid, hasParent := parentOf[id]
		if !hasParent || pid == "" {
			depthCache[id] = 0
			delete(visiting, id)
			return 0
		}
		if _, ok := spans[pid]; !ok {
			depthCache[id] = 0
			delete(visiting, id)
			return 0
		}
		d := depth(pid) + 1
		depthCache[id] = d
		delete(visiting, id)
		return d
	}
	for id := range spans {
		depth(id)
	}

	// Map error codes to their span depths.
	depths := map[string]int{}
	for _, span := range spans {
		if span.ErrorCode != "" && codes[span.ErrorCode] {
			if d := depthCache[span.SpanID]; d > depths[span.ErrorCode] {
				depths[span.ErrorCode] = d
			}
		}
	}
	return depths
}

// errorDepthsFromGraph uses error node "service" attributes and the service
// call topology (EdgeCalls BFS) to compute depth per error code.
func (d *Detector) errorDepthsFromGraph(candidates []candidate) map[string]int {
	snap := d.store.Snapshot()
	depths := map[string]int{}

	codeToSvcID := map[string]string{}
	for _, c := range candidates {
		errID := core.ID("error", c.code)
		node, ok := snap.Nodes[errID]
		if !ok {
			continue
		}
		svcName, _ := node.Attr["service"].(string)
		if svcName == "" {
			continue
		}
		env := ""
		for _, n := range snap.Nodes {
			if n.Type == core.NodeService {
				if name, _ := n.Attr["name"].(string); name == svcName {
					env, _ = n.Attr["env"].(string)
					break
				}
			}
		}
		codeToSvcID[c.code] = core.ID("service", svcName, env)
	}

	hasIncoming := map[string]bool{}
	callEdges := map[string][]string{}
	for _, edges := range snap.OutEdges {
		for _, e := range edges {
			if e.Type == core.EdgeCalls {
				callEdges[e.From] = append(callEdges[e.From], e.To)
				hasIncoming[e.To] = true
			}
		}
	}

	depthMap := map[string]int{}
	var queue []string
	for id, node := range snap.Nodes {
		if node.Type == core.NodeService && !hasIncoming[id] {
			queue = append(queue, id)
			depthMap[id] = 0
		}
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, next := range callEdges[curr] {
			if _, visited := depthMap[next]; !visited {
				depthMap[next] = depthMap[curr] + 1
				queue = append(queue, next)
			}
		}
	}

	for code, svcID := range codeToSvcID {
		depths[code] = depthMap[svcID]
	}
	return depths
}

// computeBlast computes blast radius for an error code within a time window.
func (d *Detector) computeBlast(errorCode string, start, end time.Time) (requests, users, vipUsers int, services []string, severity float64) {
	userSet := map[string]struct{}{}
	vipSet := map[string]struct{}{}
	svcSet := map[string]struct{}{}
	reqCount := 0

	d.store.ForEachRequestFact(start, end, func(f store.RequestFacts) {
		if !f.HasError(errorCode) {
			return
		}
		reqCount++
		if f.UserID != "" {
			userSet[f.UserID] = struct{}{}
		}
		if f.UserVIP && f.UserID != "" {
			vipSet[f.UserID] = struct{}{}
		}
		for _, svc := range f.Services {
			svcSet[svc] = struct{}{}
		}
	})

	svcList := make([]string, 0, len(svcSet))
	for svc := range svcSet {
		svcList = append(svcList, svc)
	}
	sort.Strings(svcList)

	sev := float64(reqCount) + float64(len(vipSet))*10 + float64(len(svcSet))*5
	return reqCount, len(userSet), len(vipSet), svcList, sev
}

// correlateDeploy checks if a recent deployment correlates with the spike.
func (d *Detector) correlateDeploy(ctx context.Context, errorCode string, now time.Time) *DeployCorrelation {
	tickCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	window := 1 * time.Hour
	deps, err := d.deploys.DeploymentsInWindow(tickCtx, now.Add(-window), now, "")
	if err != nil || len(deps) == 0 {
		return nil
	}

	snap := d.store.Snapshot()
	if len(snap.Nodes) == 0 {
		return nil
	}

	infos := make([]causal.DeploymentInfo, len(deps))
	for i, d := range deps {
		infos[i] = causal.DeploymentInfo{ID: d.ID, Service: d.Service, FirstSeen: d.FirstSeen}
	}

	claims := causal.InferIntroducedBy(snap, infos, now.Add(-window), now)
	for _, c := range claims {
		if c.Subject == errorCode {
			return &DeployCorrelation{
				DeploymentID: c.Target,
				Service:      c.Service,
				Confidence:   c.Confidence,
			}
		}
	}
	return nil
}
