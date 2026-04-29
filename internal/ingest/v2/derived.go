package ingestv2

import (
	"sort"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const LinkageDirect = "direct"

type StoryResponse struct {
	TraceID    string            `json:"trace_id"`
	Service    string            `json:"service"`
	Route      string            `json:"route"`
	Status     eventv2.Status    `json:"status"`
	Anchor     *StoryAnchor      `json:"anchor"`
	Path       []StoryStep       `json:"path"`
	Logs       []StoryLog        `json:"logs"`
	Downstream []StoryDownstream `json:"downstream"`
	Linkage    string            `json:"linkage"`
}

type StoryAnchor struct {
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
}

type StoryStep struct {
	Name       string `json:"name"`
	StartMS    int64  `json:"start_ms"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code,omitempty"`
	ErrorMsg   string `json:"error_msg,omitempty"`
}

type StoryLog struct {
	TsOffsetMS int64  `json:"ts_offset_ms"`
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	Step       string `json:"step,omitempty"`
}

type StoryDownstream struct {
	Step     string `json:"step"`
	Service  string `json:"service"`
	Endpoint string `json:"endpoint"`
}

type ErrorFamily struct {
	Service   string `json:"service"`
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
}

type ErrorRow struct {
	ErrorFamily    ErrorFamily `json:"error_family"`
	Count          int         `json:"count"`
	AffectedUsers  *int        `json:"affected_users"`
	AffectedTraces int         `json:"affected_traces"`
	SampleTraces   []string    `json:"sample_traces"`
}

type ErrorsResult struct {
	Window     string
	Rows       []ErrorRow
	NextCursor *ErrorCursor
}

type BlastKey struct {
	Service   string `json:"service,omitempty"`
	Step      string `json:"step,omitempty"`
	ErrorCode string `json:"error_code"`
}

type BlastRadiusResult struct {
	Key              BlastKey `json:"key"`
	ViewMode         string   `json:"view_mode"`
	Window           string   `json:"window"`
	AffectedRequests int      `json:"affected_requests"`
	AffectedUsers    *int     `json:"affected_users"`
	AffectedServices int      `json:"affected_services"`
	TopServices      []string `json:"top_services"`
	SampleTraces     []string `json:"sample_traces"`
}

type BlastKeyMode struct {
	Key       BlastKey
	CrossCode bool
}

func (r *Reader) TraceStoryByEventID(eventID string) (StoryResponse, bool) {
	ev, ok := r.GetEvent(eventID)
	if !ok {
		return StoryResponse{}, false
	}
	return buildStory(ev, LinkageDirect), true
}

func (r *Reader) TraceStoryByTraceID(traceID string) (StoryResponse, bool) {
	if r == nil || r.index == nil || traceID == "" {
		return StoryResponse{}, false
	}
	events := r.index.SnapshotTrace(traceID)
	if len(events) == 0 {
		return StoryResponse{}, false
	}
	resolved := ResolveAnchorWithOptions(events, ResolveOpts{ExcludeSuppressed: true})
	if resolved.Event == nil {
		return StoryResponse{}, false
	}
	return buildStory(cloneEvent(resolved.Event), resolved.Linkage), true
}

func (r *Reader) Errors(f SearchFilter, after *ErrorCursor, limit int) ErrorsResult {
	if r == nil || r.index == nil || limit <= 0 {
		return ErrorsResult{}
	}
	events := r.index.SnapshotEvents()
	traces := r.index.SnapshotTraces()
	rows := aggregateErrorRows(events, traces, f)
	sort.SliceStable(rows, func(i, j int) bool {
		return compareErrorRows(rows[i], rows[j]) < 0
	})
	out := make([]ErrorRow, 0, limit)
	hasMore := false
	for _, row := range rows {
		key := ErrorKey{Service: row.ErrorFamily.Service, Step: row.ErrorFamily.Step, ErrorCode: row.ErrorFamily.ErrorCode}
		if !afterErrorCursor(row.Count, key, after) {
			continue
		}
		if len(out) == limit {
			hasMore = true
			break
		}
		out = append(out, row)
	}
	var next *ErrorCursor
	if hasMore && len(out) > 0 {
		last := out[len(out)-1]
		next = &ErrorCursor{
			Count:     last.Count,
			Service:   last.ErrorFamily.Service,
			Step:      last.ErrorFamily.Step,
			ErrorCode: last.ErrorFamily.ErrorCode,
		}
	}
	return ErrorsResult{Window: timeWindowString(f.Since, f.Until), Rows: out, NextCursor: next}
}

func (r *Reader) BlastRadius(f SearchFilter, key BlastKeyMode) BlastRadiusResult {
	if r == nil || r.index == nil {
		return BlastRadiusResult{Key: key.Key, Window: timeWindowString(f.Since, f.Until), TopServices: []string{}, SampleTraces: []string{}}
	}
	events := r.index.SnapshotEvents()
	traces := r.index.SnapshotTraces()
	matchedTraceLatest := map[string]time.Time{}
	for _, ev := range events {
		if !eventMatchesBlastKey(ev, f, key) {
			continue
		}
		if latest, ok := matchedTraceLatest[ev.TraceID]; !ok || ev.TsStart.After(latest) {
			matchedTraceLatest[ev.TraceID] = ev.TsStart
		}
	}
	users := collectUsersForTraces(traces, matchedTraceLatest)
	services := countServicesForTraces(traces, matchedTraceLatest)
	affectedUsers := nullableCount(len(users), len(users) > 0)
	viewMode := "single_family"
	if key.CrossCode {
		viewMode = "cross_family"
	}
	return BlastRadiusResult{
		Key:              key.Key,
		ViewMode:         viewMode,
		Window:           timeWindowString(f.Since, f.Until),
		AffectedRequests: len(matchedTraceLatest),
		AffectedUsers:    affectedUsers,
		AffectedServices: len(services),
		TopServices:      topServices(services, 10),
		SampleTraces:     sampleTraces(matchedTraceLatest, 3),
	}
}

func buildStory(ev *eventv2.Event, linkage string) StoryResponse {
	out := StoryResponse{
		TraceID:    ev.TraceID,
		Service:    ev.Service,
		Route:      routeFromV2Fields(ev.Fields),
		Status:     ev.Status,
		Path:       []StoryStep{},
		Logs:       []StoryLog{},
		Downstream: []StoryDownstream{},
		Linkage:    linkage,
	}
	if ev.Status == eventv2.StatusSuppressed {
		return out
	}
	if ev.Anchor != nil {
		out.Anchor = &StoryAnchor{Step: ev.Anchor.Step, ErrorCode: ev.Anchor.ErrorCode}
	}
	steps, anchorEnd, anchored := contributingSteps(ev)
	for _, step := range steps {
		out.Path = append(out.Path, storyStep(step))
		if step.Downstream != nil {
			out.Downstream = append(out.Downstream, StoryDownstream{
				Step:     step.Name,
				Service:  step.Downstream.Service,
				Endpoint: step.Downstream.Endpoint,
			})
		}
	}
	firstStart := int64(0)
	if len(steps) > 0 {
		firstStart = steps[0].StartMS
	}
	for _, log := range ev.Logs {
		if log.Level != "warn" && log.Level != "error" {
			continue
		}
		if anchored && (log.TsOffsetMS < firstStart || log.TsOffsetMS > anchorEnd) {
			continue
		}
		out.Logs = append(out.Logs, StoryLog{TsOffsetMS: log.TsOffsetMS, Level: log.Level, Msg: log.Msg})
	}
	return out
}

func contributingSteps(ev *eventv2.Event) ([]eventv2.Step, int64, bool) {
	steps := append([]eventv2.Step(nil), ev.Steps...)
	if ev.Anchor == nil {
		sortSteps(steps)
		return steps, 0, false
	}
	anchorStep, ok := latestAnchorStep(steps, ev.Anchor.Step)
	if !ok {
		sortSteps(steps)
		return steps, 0, false
	}
	anchorEnd := anchorStep.StartMS + anchorStep.DurationMS
	out := make([]eventv2.Step, 0, len(steps))
	for _, step := range steps {
		if step.StartMS+step.DurationMS <= anchorEnd {
			out = append(out, step)
		}
	}
	sortSteps(out)
	return out, anchorEnd, true
}

func latestAnchorStep(steps []eventv2.Step, name string) (eventv2.Step, bool) {
	var fallback eventv2.Step
	var latest eventv2.Step
	hasFallback := false
	hasLatest := false
	for _, step := range steps {
		if step.Name != name {
			continue
		}
		if !hasFallback || step.StartMS > fallback.StartMS {
			fallback = step
			hasFallback = true
		}
		if step.Status == "error" && (!hasLatest || step.StartMS > latest.StartMS) {
			latest = step
			hasLatest = true
		}
	}
	if hasLatest {
		return latest, true
	}
	return fallback, hasFallback
}

func sortSteps(steps []eventv2.Step) {
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].StartMS != steps[j].StartMS {
			return steps[i].StartMS < steps[j].StartMS
		}
		return i < j
	})
}

func storyStep(step eventv2.Step) StoryStep {
	out := StoryStep{Name: step.Name, StartMS: step.StartMS, DurationMS: step.DurationMS, Status: step.Status}
	if step.Error != nil {
		out.ErrorCode = step.Error.Code
		out.ErrorMsg = step.Error.Reason
	}
	return out
}

type errorAgg struct {
	key         ErrorKey
	count       int
	traceLatest map[string]time.Time
}

func aggregateErrorRows(events []*eventv2.Event, traces map[string][]*eventv2.Event, f SearchFilter) []ErrorRow {
	aggs := map[ErrorKey]*errorAgg{}
	for _, ev := range events {
		if !eventMatchesErrorRollup(ev, f) {
			continue
		}
		key := ErrorKey{Service: ev.Service, Step: ev.Anchor.Step, ErrorCode: ev.Anchor.ErrorCode}
		agg := aggs[key]
		if agg == nil {
			agg = &errorAgg{key: key, traceLatest: map[string]time.Time{}}
			aggs[key] = agg
		}
		agg.count++
		if latest, ok := agg.traceLatest[ev.TraceID]; !ok || ev.TsStart.After(latest) {
			agg.traceLatest[ev.TraceID] = ev.TsStart
		}
	}
	rows := make([]ErrorRow, 0, len(aggs))
	for _, agg := range aggs {
		users := collectUsersForTraces(traces, agg.traceLatest)
		rows = append(rows, ErrorRow{
			ErrorFamily:    ErrorFamily{Service: agg.key.Service, Step: agg.key.Step, ErrorCode: agg.key.ErrorCode},
			Count:          agg.count,
			AffectedUsers:  nullableCount(len(users), len(users) > 0),
			AffectedTraces: len(agg.traceLatest),
			SampleTraces:   sampleTraces(agg.traceLatest, 3),
		})
	}
	return rows
}

func eventMatchesErrorRollup(ev *eventv2.Event, f SearchFilter) bool {
	if ev == nil || ev.Anchor == nil || !isFailedStatus(ev.Status) {
		return false
	}
	if len(f.Statuses) > 0 {
		if _, ok := f.Statuses[ev.Status]; !ok {
			return false
		}
	}
	if f.Service != "" && ev.Service != f.Service {
		return false
	}
	return eventWithinWindow(ev, f)
}

func eventMatchesBlastKey(ev *eventv2.Event, f SearchFilter, key BlastKeyMode) bool {
	if ev == nil || ev.Anchor == nil || !isFailedStatus(ev.Status) || !eventWithinWindow(ev, f) {
		return false
	}
	if key.Key.ErrorCode != ev.Anchor.ErrorCode {
		return false
	}
	if key.CrossCode {
		return true
	}
	return ev.Service == key.Key.Service && ev.Anchor.Step == key.Key.Step
}

func eventWithinWindow(ev *eventv2.Event, f SearchFilter) bool {
	if !f.Since.IsZero() && ev.TsStart.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && ev.TsStart.After(f.Until) {
		return false
	}
	return true
}

func compareErrorRows(a, b ErrorRow) int {
	if a.Count != b.Count {
		return b.Count - a.Count
	}
	if a.ErrorFamily.Service != b.ErrorFamily.Service {
		if a.ErrorFamily.Service < b.ErrorFamily.Service {
			return -1
		}
		return 1
	}
	if a.ErrorFamily.Step != b.ErrorFamily.Step {
		if a.ErrorFamily.Step < b.ErrorFamily.Step {
			return -1
		}
		return 1
	}
	if a.ErrorFamily.ErrorCode < b.ErrorFamily.ErrorCode {
		return -1
	}
	if a.ErrorFamily.ErrorCode > b.ErrorFamily.ErrorCode {
		return 1
	}
	return 0
}

func collectUsersForTraces(traces map[string][]*eventv2.Event, traceSet map[string]time.Time) map[string]struct{} {
	users := map[string]struct{}{}
	for traceID := range traceSet {
		for _, ev := range traces[traceID] {
			if userID := userIDFromFields(ev.Fields); userID != "" {
				users[userID] = struct{}{}
			}
		}
	}
	return users
}

func countServicesForTraces(traces map[string][]*eventv2.Event, traceSet map[string]time.Time) map[string]int {
	services := map[string]int{}
	for traceID := range traceSet {
		seen := map[string]struct{}{}
		for _, ev := range traces[traceID] {
			if ev == nil || ev.Service == "" {
				continue
			}
			seen[ev.Service] = struct{}{}
		}
		for service := range seen {
			services[service]++
		}
	}
	return services
}

func sampleTraces(traceLatest map[string]time.Time, limit int) []string {
	type item struct {
		traceID string
		ts      time.Time
	}
	items := make([]item, 0, len(traceLatest))
	for traceID, ts := range traceLatest {
		items = append(items, item{traceID: traceID, ts: ts})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].ts.Equal(items[j].ts) {
			return items[i].ts.After(items[j].ts)
		}
		return items[i].traceID < items[j].traceID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.traceID)
	}
	return out
}

func topServices(counts map[string]int, limit int) []string {
	type item struct {
		service string
		count   int
	}
	items := make([]item, 0, len(counts))
	for service, count := range counts {
		items = append(items, item{service: service, count: count})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].service < items[j].service
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.service)
	}
	return out
}

func nullableCount(n int, known bool) *int {
	if !known {
		return nil
	}
	v := n
	return &v
}

func userIDFromFields(fields map[string]any) string {
	user, ok := fields["user"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := user["id"].(string)
	return id
}

func routeFromV2Fields(fields map[string]any) string {
	httpFields, ok := fields["http"].(map[string]any)
	if !ok {
		return ""
	}
	route, _ := httpFields["route"].(string)
	return route
}

func parseErrorFamilyDisplay(s string) (BlastKey, bool) {
	parts := make([]string, 0, 3)
	var current []rune
	escaped := false
	for _, r := range s {
		if escaped {
			if r != ':' && r != '\\' {
				return BlastKey{}, false
			}
			current = append(current, r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case ':':
			parts = append(parts, string(current))
			current = current[:0]
		default:
			current = append(current, r)
		}
	}
	if escaped {
		return BlastKey{}, false
	}
	parts = append(parts, string(current))
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return BlastKey{}, false
	}
	return BlastKey{Service: parts[0], Step: parts[1], ErrorCode: parts[2]}, true
}

func timeWindowString(since, until time.Time) string {
	if since.IsZero() || until.IsZero() || until.Before(since) {
		return ""
	}
	return until.Sub(since).String()
}
