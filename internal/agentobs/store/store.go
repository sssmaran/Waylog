package store

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

type RunInfo struct {
	RunID              string
	RootSessionID      string
	StartTime          time.Time
	EndTime            time.Time
	State              string
	LastEventAt        time.Time
	DurationMs         int64
	InclusiveTokensIn  int
	InclusiveTokensOut int
	SessionCount       int
	Success            bool
}

type SessionInfo struct {
	SessionID          string
	RunID              string
	ParentSessionID    string
	TriggerStepID      string
	AgentName          string
	AgentVersion       string
	Prompt             string
	StartTime          time.Time
	EndTime            time.Time
	State              string
	LastEventAt        time.Time
	DurationMs         int64
	ExclusiveTokensIn  int
	ExclusiveTokensOut int
	InclusiveTokensIn  int
	InclusiveTokensOut int
	TotalSteps         int
	Success            bool
}

type StepInfo struct {
	StepID     string
	StepName   string
	SessionID  string
	StepIndex  int
	Model      string
	TokensIn   int
	TokensOut  int
	ToolName   string
	ToolInput  string
	ToolOutput string
	ToolError  string
	LatencyMs  int64
	OffsetMs   int64
	Started    bool
	Ended      bool
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
