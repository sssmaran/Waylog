package store

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

type RunInfo struct {
	RunID              string    `json:"run_id"`
	RootSessionID      string    `json:"root_session_id"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	State              string    `json:"state"`
	LastEventAt        time.Time `json:"last_event_at"`
	DurationMs         int64     `json:"duration_ms"`
	InclusiveTokensIn  int       `json:"inclusive_tokens_in"`
	InclusiveTokensOut int       `json:"inclusive_tokens_out"`
	SessionCount       int       `json:"session_count"`
	Success            bool      `json:"success"`
}

type SessionInfo struct {
	SessionID          string    `json:"session_id"`
	RunID              string    `json:"run_id"`
	ParentSessionID    string    `json:"parent_session_id"`
	TriggerStepID      string    `json:"trigger_step_id"`
	AgentName          string    `json:"agent_name"`
	AgentVersion       string    `json:"agent_version"`
	Prompt             string    `json:"prompt"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	State              string    `json:"state"`
	LastEventAt        time.Time `json:"last_event_at"`
	DurationMs         int64     `json:"duration_ms"`
	ExclusiveTokensIn  int       `json:"exclusive_tokens_in"`
	ExclusiveTokensOut int       `json:"exclusive_tokens_out"`
	InclusiveTokensIn  int       `json:"inclusive_tokens_in"`
	InclusiveTokensOut int       `json:"inclusive_tokens_out"`
	TotalSteps         int       `json:"total_steps"`
	Success            bool      `json:"success"`
}

type StepInfo struct {
	StepID     string `json:"step_id"`
	StepName   string `json:"step_name"`
	SessionID  string `json:"session_id"`
	StepIndex  int    `json:"step_index"`
	Model      string `json:"model"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	ToolName   string `json:"tool_name"`
	ToolInput  string `json:"tool_input"`
	ToolOutput string `json:"tool_output"`
	ToolError  string `json:"tool_error"`
	LatencyMs  int64  `json:"latency_ms"`
	OffsetMs   int64  `json:"offset_ms"`
	Started    bool   `json:"started"`
	Ended      bool   `json:"ended"`
}

type Store struct {
	mu            sync.RWMutex
	runs          map[string]*RunInfo
	sessions      map[string]*SessionInfo
	steps         map[string]*StepInfo
	runSessions   map[string][]string
	sessionSteps  map[string][]string
	childSessions map[string][]string
}

func New() *Store {
	return &Store{
		runs:          make(map[string]*RunInfo),
		sessions:      make(map[string]*SessionInfo),
		steps:         make(map[string]*StepInfo),
		runSessions:   make(map[string][]string),
		sessionSteps:  make(map[string][]string),
		childSessions: make(map[string][]string),
	}
}

func stepKey(sessionID string, stepIndex int) string {
	return fmt.Sprintf("%s:%d", sessionID, stepIndex)
}

func (s *Store) Merge(ev *agentobs.AgentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch ev.EventType {
	case agentobs.EventRunStart:
		return s.mergeRunStart(ev)
	case agentobs.EventRunEnd:
		return s.mergeRunEnd(ev)
	case agentobs.EventSessionStart:
		return s.mergeSessionStart(ev)
	case agentobs.EventSessionEnd:
		return s.mergeSessionEnd(ev)
	case agentobs.EventSessionHeartbeat:
		return s.mergeSessionHeartbeat(ev)
	case agentobs.EventStepStart:
		return s.mergeStepStart(ev)
	case agentobs.EventStepEnd:
		return s.mergeStepEnd(ev)
	default:
		return fmt.Errorf("unknown event type %q", ev.EventType)
	}
}

func (s *Store) ensureRun(runID string, ts time.Time) *RunInfo {
	run, ok := s.runs[runID]
	if !ok {
		run = &RunInfo{
			RunID:     runID,
			StartTime: ts,
			State:     agentobs.StateActive,
		}
		s.runs[runID] = run
	}
	return run
}

func (s *Store) mergeRunStart(ev *agentobs.AgentEvent) error {
	run := s.ensureRun(ev.RunID, ev.Timestamp)
	// backfill true start time if run was auto-created by a later event
	if ev.Timestamp.Before(run.StartTime) {
		run.StartTime = ev.Timestamp
	}
	run.LastEventAt = ev.Timestamp
	return nil
}

func (s *Store) mergeRunEnd(ev *agentobs.AgentEvent) error {
	run := s.ensureRun(ev.RunID, ev.Timestamp)

	if agentobs.IsTerminalState(run.State) {
		return nil // idempotent
	}

	if ev.Success {
		run.State = agentobs.StateCompleted
		run.Success = true
	} else {
		run.State = agentobs.StateFailed
	}
	run.EndTime = ev.Timestamp
	run.DurationMs = ev.Timestamp.Sub(run.StartTime).Milliseconds()
	run.LastEventAt = ev.Timestamp

	s.rollupRunCosts(run)
	return nil
}

func (s *Store) mergeSessionStart(ev *agentobs.AgentEvent) error {
	run := s.ensureRun(ev.RunID, ev.Timestamp)

	if agentobs.IsTerminalState(run.State) {
		return fmt.Errorf("RUN_TERMINATED")
	}

	if _, exists := s.sessions[ev.SessionID]; exists {
		return nil // idempotent
	}

	sess := &SessionInfo{
		SessionID:       ev.SessionID,
		RunID:           ev.RunID,
		ParentSessionID: ev.ParentSessionID,
		TriggerStepID:   ev.TriggerStepID,
		AgentName:       ev.AgentName,
		AgentVersion:    ev.AgentVersion,
		Prompt:          ev.Prompt,
		StartTime:       ev.Timestamp,
		State:           agentobs.StateActive,
		LastEventAt:     ev.Timestamp,
	}
	s.sessions[ev.SessionID] = sess
	s.runSessions[ev.RunID] = append(s.runSessions[ev.RunID], ev.SessionID)

	if ev.ParentSessionID != "" {
		s.childSessions[ev.ParentSessionID] = append(s.childSessions[ev.ParentSessionID], ev.SessionID)
	}

	// set root session on run if first parentless session
	if ev.ParentSessionID == "" && run.RootSessionID == "" {
		run.RootSessionID = ev.SessionID
	}

	run.SessionCount++
	run.LastEventAt = ev.Timestamp
	return nil
}

func (s *Store) mergeSessionEnd(ev *agentobs.AgentEvent) error {
	sess, ok := s.sessions[ev.SessionID]
	if !ok {
		return nil // ignore unknown session
	}

	if agentobs.IsTerminalState(sess.State) {
		return nil // idempotent
	}

	if ev.Success {
		sess.State = agentobs.StateCompleted
		sess.Success = true
	} else {
		sess.State = agentobs.StateFailed
	}
	sess.EndTime = ev.Timestamp
	sess.DurationMs = ev.Timestamp.Sub(sess.StartTime).Milliseconds()
	sess.LastEventAt = ev.Timestamp

	s.rollupSessionCosts(sess)

	run := s.runs[sess.RunID]
	if run != nil {
		run.LastEventAt = ev.Timestamp
		s.checkRunAutoComplete(run)
	}
	return nil
}

func (s *Store) mergeSessionHeartbeat(ev *agentobs.AgentEvent) error {
	sess, ok := s.sessions[ev.SessionID]
	if !ok {
		return nil
	}

	if agentobs.IsTerminalState(sess.State) {
		return fmt.Errorf("SESSION_TERMINATED")
	}

	// revive abandoned
	if sess.State == agentobs.StateAbandoned {
		sess.State = agentobs.StateActive
	}
	sess.LastEventAt = ev.Timestamp

	run := s.runs[sess.RunID]
	if run != nil {
		if run.State == agentobs.StateAbandoned {
			run.State = agentobs.StateActive
		}
		run.LastEventAt = ev.Timestamp
	}
	return nil
}

func (s *Store) mergeStepStart(ev *agentobs.AgentEvent) error {
	sess, ok := s.sessions[ev.SessionID]
	if !ok {
		return fmt.Errorf("SESSION_NOT_FOUND")
	}

	if agentobs.IsTerminalState(sess.State) {
		return fmt.Errorf("SESSION_TERMINATED")
	}

	// revive abandoned
	if sess.State == agentobs.StateAbandoned {
		sess.State = agentobs.StateActive
	}

	key := stepKey(ev.SessionID, ev.StepIndex)
	if existing, ok := s.steps[key]; ok {
		if existing.StepID != ev.StepID {
			return fmt.Errorf("STEP_ID_MISMATCH: index %d has step_id %q, got %q", ev.StepIndex, existing.StepID, ev.StepID)
		}
		// backfill start metadata if step.end arrived first
		if !existing.Started {
			existing.Started = true
			existing.StepName = ev.StepName
			if !sess.StartTime.IsZero() {
				existing.OffsetMs = ev.Timestamp.Sub(sess.StartTime).Milliseconds()
			}
		}
		return nil
	}

	var offsetMs int64
	if !sess.StartTime.IsZero() {
		offsetMs = ev.Timestamp.Sub(sess.StartTime).Milliseconds()
	}

	step := &StepInfo{
		StepID:    ev.StepID,
		StepName:  ev.StepName,
		SessionID: ev.SessionID,
		StepIndex: ev.StepIndex,
		OffsetMs:  offsetMs,
		Started:   true,
	}
	s.steps[key] = step
	s.sessionSteps[ev.SessionID] = append(s.sessionSteps[ev.SessionID], key)

	if ev.StepIndex+1 > sess.TotalSteps {
		sess.TotalSteps = ev.StepIndex + 1
	}
	sess.LastEventAt = ev.Timestamp
	return nil
}

func (s *Store) mergeStepEnd(ev *agentobs.AgentEvent) error {
	sess, ok := s.sessions[ev.SessionID]
	if !ok {
		return fmt.Errorf("SESSION_NOT_FOUND")
	}

	if agentobs.IsTerminalState(sess.State) {
		return fmt.Errorf("SESSION_TERMINATED")
	}

	key := stepKey(ev.SessionID, ev.StepIndex)
	step, exists := s.steps[key]
	if exists && step.StepID != ev.StepID {
		return fmt.Errorf("STEP_ID_MISMATCH: index %d has step_id %q, got %q", ev.StepIndex, step.StepID, ev.StepID)
	}
	if !exists {
		// auto-create (step.start may not have arrived)
		// derive approximate start offset from end timestamp - latency
		var offsetMs int64
		if !sess.StartTime.IsZero() && ev.LatencyMs > 0 {
			approxStart := ev.Timestamp.Add(-time.Duration(ev.LatencyMs) * time.Millisecond)
			offsetMs = approxStart.Sub(sess.StartTime).Milliseconds()
			if offsetMs < 0 {
				offsetMs = 0
			}
		}
		step = &StepInfo{
			StepID:    ev.StepID,
			SessionID: ev.SessionID,
			StepIndex: ev.StepIndex,
			OffsetMs:  offsetMs,
		}
		s.steps[key] = step
		s.sessionSteps[ev.SessionID] = append(s.sessionSteps[ev.SessionID], key)

		if ev.StepIndex+1 > sess.TotalSteps {
			sess.TotalSteps = ev.StepIndex + 1
		}
	}

	if step.Ended {
		return nil // idempotent
	}

	step.Ended = true
	step.Model = ev.Model
	step.TokensIn = ev.TokensIn
	step.TokensOut = ev.TokensOut
	step.ToolName = ev.ToolName
	step.ToolInput = ev.ToolInput
	step.ToolOutput = ev.ToolOutput
	step.ToolError = ev.ToolError
	step.LatencyMs = ev.LatencyMs
	if ev.StepName != "" {
		step.StepName = ev.StepName
	}

	// accumulate exclusive tokens on session
	sess.ExclusiveTokensIn += ev.TokensIn
	sess.ExclusiveTokensOut += ev.TokensOut
	sess.LastEventAt = ev.Timestamp
	return nil
}

func (s *Store) rollupSessionCosts(sess *SessionInfo) {
	sess.InclusiveTokensIn = sess.ExclusiveTokensIn
	sess.InclusiveTokensOut = sess.ExclusiveTokensOut

	for _, childID := range s.childSessions[sess.SessionID] {
		child, ok := s.sessions[childID]
		if !ok {
			continue
		}
		// ensure child is rolled up
		if child.InclusiveTokensIn == 0 && child.InclusiveTokensOut == 0 {
			s.rollupSessionCosts(child)
		}
		sess.InclusiveTokensIn += child.InclusiveTokensIn
		sess.InclusiveTokensOut += child.InclusiveTokensOut
	}
}

func (s *Store) rollupRunCosts(run *RunInfo) {
	if run.RootSessionID == "" {
		return
	}
	root, ok := s.sessions[run.RootSessionID]
	if !ok {
		return
	}
	s.rollupSessionCosts(root)
	run.InclusiveTokensIn = root.InclusiveTokensIn
	run.InclusiveTokensOut = root.InclusiveTokensOut
}

func (s *Store) checkRunAutoComplete(run *RunInfo) {
	if agentobs.IsTerminalState(run.State) {
		return
	}

	sessionIDs := s.runSessions[run.RunID]
	if len(sessionIDs) == 0 {
		return
	}

	anyFailed := false
	for _, sid := range sessionIDs {
		sess, ok := s.sessions[sid]
		if !ok {
			return
		}
		if !agentobs.IsTerminalState(sess.State) && sess.State != agentobs.StateAbandoned {
			return // still active sessions
		}
		if sess.State == agentobs.StateAbandoned {
			return // abandoned is not terminal for auto-complete
		}
		if sess.State == agentobs.StateFailed {
			anyFailed = true
		}
	}

	// use the latest session end time as the run end time
	var latestEnd time.Time
	for _, sid := range sessionIDs {
		sess := s.sessions[sid]
		if sess.EndTime.After(latestEnd) {
			latestEnd = sess.EndTime
		}
	}

	if anyFailed {
		run.State = agentobs.StateFailed
	} else {
		run.State = agentobs.StateCompleted
		run.Success = true
	}
	run.EndTime = latestEnd
	run.DurationMs = latestEnd.Sub(run.StartTime).Milliseconds()
	run.LastEventAt = latestEnd

	// rollup costs on auto-complete
	s.rollupRunCosts(run)
}

// --- Getters ---

func (s *Store) GetRun(runID string) (RunInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return RunInfo{}, false
	}
	return *run, true
}

func (s *Store) GetSession(sessionID string) (SessionInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return SessionInfo{}, false
	}
	return *sess, true
}

func (s *Store) GetSteps(sessionID string) []StepInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := s.sessionSteps[sessionID]
	out := make([]StepInfo, 0, len(keys))
	for _, k := range keys {
		if step, ok := s.steps[k]; ok {
			out = append(out, *step)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StepIndex < out[j].StepIndex
	})
	return out
}

func (s *Store) RunCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runs)
}

func (s *Store) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// --- Mutation ---

func (s *Store) MarkAbandoned(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || agentobs.IsTerminalState(sess.State) {
		return
	}
	sess.State = agentobs.StateAbandoned
}

type SnapshotData struct {
	Runs          map[string]*RunInfo     `json:"runs"`
	Sessions      map[string]*SessionInfo `json:"sessions"`
	Steps         map[string]*StepInfo    `json:"steps"`
	RunSessions   map[string][]string     `json:"run_sessions"`
	SessionSteps  map[string][]string     `json:"session_steps"`
	ChildSessions map[string][]string     `json:"child_sessions"`
}

func (s *Store) Snapshot() *SnapshotData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runs := make(map[string]*RunInfo, len(s.runs))
	for k, v := range s.runs {
		cp := *v
		runs[k] = &cp
	}

	sessions := make(map[string]*SessionInfo, len(s.sessions))
	for k, v := range s.sessions {
		cp := *v
		sessions[k] = &cp
	}

	steps := make(map[string]*StepInfo, len(s.steps))
	for k, v := range s.steps {
		cp := *v
		steps[k] = &cp
	}

	runSessions := make(map[string][]string, len(s.runSessions))
	for k, v := range s.runSessions {
		cp := make([]string, len(v))
		copy(cp, v)
		runSessions[k] = cp
	}

	sessionSteps := make(map[string][]string, len(s.sessionSteps))
	for k, v := range s.sessionSteps {
		cp := make([]string, len(v))
		copy(cp, v)
		sessionSteps[k] = cp
	}

	childSessions := make(map[string][]string, len(s.childSessions))
	for k, v := range s.childSessions {
		cp := make([]string, len(v))
		copy(cp, v)
		childSessions[k] = cp
	}

	return &SnapshotData{
		Runs:          runs,
		Sessions:      sessions,
		Steps:         steps,
		RunSessions:   runSessions,
		SessionSteps:  sessionSteps,
		ChildSessions: childSessions,
	}
}

func (s *Store) Restore(data *SnapshotData) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if data.Runs != nil {
		s.runs = data.Runs
	}
	if data.Sessions != nil {
		s.sessions = data.Sessions
	}
	if data.Steps != nil {
		s.steps = data.Steps
	}
	if data.RunSessions != nil {
		s.runSessions = data.RunSessions
	}
	if data.SessionSteps != nil {
		s.sessionSteps = data.SessionSteps
	}
	if data.ChildSessions != nil {
		s.childSessions = data.ChildSessions
	}
}

// --- Query Methods ---

func (s *Store) ListRuns(limit int, status string, before time.Time) []RunInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []RunInfo
	for _, run := range s.runs {
		if status != "" && run.State != status {
			continue
		}
		if !before.IsZero() && !run.StartTime.Before(before) {
			continue
		}
		all = append(all, *run)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartTime.After(all[j].StartTime)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

func (s *Store) GetRunSessions(runID string) []SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sids := s.runSessions[runID]
	out := make([]SessionInfo, 0, len(sids))
	for _, sid := range sids {
		if sess, ok := s.sessions[sid]; ok {
			out = append(out, *sess)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartTime.Before(out[j].StartTime)
	})
	return out
}

func (s *Store) ScanAbandoned(threshold time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-threshold)
	for _, sess := range s.sessions {
		if sess.State == agentobs.StateActive && sess.LastEventAt.Before(cutoff) {
			sess.State = agentobs.StateAbandoned
		}
	}
	for _, run := range s.runs {
		if run.State == agentobs.StateActive && run.LastEventAt.Before(cutoff) {
			run.State = agentobs.StateAbandoned
		}
	}
}

type Stats struct {
	RunCount      int     `json:"run_count"`
	ActiveCount   int     `json:"active_count"`
	CompletedCount int    `json:"completed_count"`
	FailedCount   int     `json:"failed_count"`
	SessionCount  int     `json:"session_count"`
	StepCount     int     `json:"step_count"`
	AvgTokensIn   float64 `json:"avg_tokens_in"`
	AvgTokensOut  float64 `json:"avg_tokens_out"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

func (s *Store) AggregateStats(window time.Duration) Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	var st Stats
	var totalTokensIn, totalTokensOut, totalDuration int64
	var durationCount int
	for _, run := range s.runs {
		if run.StartTime.Before(cutoff) {
			continue
		}
		st.RunCount++
		switch run.State {
		case agentobs.StateActive:
			st.ActiveCount++
		case agentobs.StateCompleted:
			st.CompletedCount++
		case agentobs.StateFailed:
			st.FailedCount++
		}
		if run.DurationMs > 0 {
			totalDuration += run.DurationMs
			durationCount++
		}
	}
	for _, sess := range s.sessions {
		if sess.StartTime.Before(cutoff) {
			continue
		}
		st.SessionCount++
		totalTokensIn += int64(sess.ExclusiveTokensIn)
		totalTokensOut += int64(sess.ExclusiveTokensOut)
	}
	for _, step := range s.steps {
		sess, ok := s.sessions[step.SessionID]
		if !ok || sess.StartTime.Before(cutoff) {
			continue
		}
		st.StepCount++
	}
	if st.SessionCount > 0 {
		st.AvgTokensIn = float64(totalTokensIn) / float64(st.SessionCount)
		st.AvgTokensOut = float64(totalTokensOut) / float64(st.SessionCount)
	}
	if durationCount > 0 {
		st.AvgDurationMs = float64(totalDuration) / float64(durationCount)
	}
	return st
}

type ToolStat struct {
	ToolName     string  `json:"tool_name"`
	CallCount    int     `json:"call_count"`
	SuccessCount int     `json:"success_count"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P95LatencyMs float64 `json:"p95_latency_ms"`
}

func (s *Store) ToolAnalytics(window time.Duration) []ToolStat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	type toolAccum struct {
		calls     int
		success   int
		latencies []float64
	}
	accum := make(map[string]*toolAccum)
	for _, step := range s.steps {
		if step.ToolName == "" || !step.Ended {
			continue
		}
		sess, ok := s.sessions[step.SessionID]
		if !ok || sess.StartTime.Before(cutoff) {
			continue
		}
		a, ok := accum[step.ToolName]
		if !ok {
			a = &toolAccum{}
			accum[step.ToolName] = a
		}
		a.calls++
		if step.ToolError == "" {
			a.success++
		}
		a.latencies = append(a.latencies, float64(step.LatencyMs))
	}
	out := make([]ToolStat, 0, len(accum))
	for name, a := range accum {
		ts := ToolStat{ToolName: name, CallCount: a.calls, SuccessCount: a.success}
		if a.calls > 0 {
			ts.SuccessRate = float64(a.success) / float64(a.calls)
			var sum float64
			for _, l := range a.latencies {
				sum += l
			}
			ts.AvgLatencyMs = sum / float64(a.calls)
			sort.Float64s(a.latencies)
			idx := int(float64(len(a.latencies)) * 0.95)
			if idx >= len(a.latencies) {
				idx = len(a.latencies) - 1
			}
			ts.P95LatencyMs = a.latencies[idx]
		}
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallCount > out[j].CallCount })
	return out
}

type TokenCount struct {
	TokensIn  int `json:"tokens_in"`
	TokensOut int `json:"tokens_out"`
}

func (s *Store) TokensByModel(window time.Duration) map[string]TokenCount {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	result := make(map[string]TokenCount)
	for _, step := range s.steps {
		if step.Model == "" || !step.Ended {
			continue
		}
		sess, ok := s.sessions[step.SessionID]
		if !ok || sess.StartTime.Before(cutoff) {
			continue
		}
		tc := result[step.Model]
		tc.TokensIn += step.TokensIn
		tc.TokensOut += step.TokensOut
		result[step.Model] = tc
	}
	return result
}

func (s *Store) PruneOlderThan(retention time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-retention)

	for runID, run := range s.runs {
		// never prune active runs
		if run.State == agentobs.StateActive {
			continue
		}
		if run.LastEventAt.After(cutoff) {
			continue
		}

		// remove all sessions and steps for this run
		for _, sid := range s.runSessions[runID] {
			for _, sk := range s.sessionSteps[sid] {
				delete(s.steps, sk)
			}
			delete(s.sessionSteps, sid)
			delete(s.childSessions, sid)
			delete(s.sessions, sid)
		}
		delete(s.runSessions, runID)
		delete(s.runs, runID)
	}
}
