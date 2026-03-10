package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

func ev(eventType, runID string, ts time.Time) *agentobs.AgentEvent {
	return &agentobs.AgentEvent{
		EventID:       eventType + "-" + runID,
		RunID:         runID,
		EventType:     eventType,
		Timestamp:     ts,
		SchemaVersion: "1.0",
	}
}

func TestStore_MergeRunStart(t *testing.T) {
	s := New()
	now := time.Now()

	e := ev(agentobs.EventRunStart, "r1", now)
	if err := s.Merge(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	run, ok := s.GetRun("r1")
	if !ok {
		t.Fatal("run not found")
	}
	if run.State != agentobs.StateActive {
		t.Fatalf("want state active, got %s", run.State)
	}
	if run.RunID != "r1" {
		t.Fatalf("want runID r1, got %s", run.RunID)
	}
	if s.RunCount() != 1 {
		t.Fatalf("want 1 run, got %d", s.RunCount())
	}
}

func TestStore_SessionLifecycle(t *testing.T) {
	s := New()
	now := time.Now()

	// run.start
	if err := s.Merge(ev(agentobs.EventRunStart, "r1", now)); err != nil {
		t.Fatal(err)
	}

	// session.start
	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "test-agent"
	if err := s.Merge(e); err != nil {
		t.Fatal(err)
	}

	sess, ok := s.GetSession("s1")
	if !ok {
		t.Fatal("session not found")
	}
	if sess.State != agentobs.StateActive {
		t.Fatalf("want active, got %s", sess.State)
	}

	// session.end success
	e2 := ev(agentobs.EventSessionEnd, "r1", now.Add(2*time.Second))
	e2.SessionID = "s1"
	e2.Success = true
	if err := s.Merge(e2); err != nil {
		t.Fatal(err)
	}

	sess, _ = s.GetSession("s1")
	if sess.State != agentobs.StateCompleted {
		t.Fatalf("want completed, got %s", sess.State)
	}
	if !sess.Success {
		t.Fatal("want success=true")
	}
}

func TestStore_TerminalSessionRejectsEvents(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// end session
	e2 := ev(agentobs.EventSessionEnd, "r1", now.Add(2*time.Second))
	e2.SessionID = "s1"
	e2.Success = true
	s.Merge(e2)

	// step.start on terminal session should fail
	e3 := ev(agentobs.EventStepStart, "r1", now.Add(3*time.Second))
	e3.SessionID = "s1"
	e3.StepID = "st1"
	e3.StepIndex = 0
	err := s.Merge(e3)
	if err == nil {
		t.Fatal("expected error for step on terminal session")
	}
	if !strings.Contains(err.Error(), "SESSION_TERMINATED") {
		t.Fatalf("want SESSION_TERMINATED, got %v", err)
	}
}

func TestStore_AbandonedSessionRevives(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	s.MarkAbandoned("s1")

	sess, _ := s.GetSession("s1")
	if sess.State != agentobs.StateAbandoned {
		t.Fatalf("want abandoned, got %s", sess.State)
	}

	// heartbeat revives
	e2 := ev(agentobs.EventSessionHeartbeat, "r1", now.Add(5*time.Second))
	e2.SessionID = "s1"
	if err := s.Merge(e2); err != nil {
		t.Fatalf("heartbeat should revive abandoned session: %v", err)
	}

	sess, _ = s.GetSession("s1")
	if sess.State != agentobs.StateActive {
		t.Fatalf("want active after revive, got %s", sess.State)
	}
}

func TestStore_StepTracking(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// step.start
	e2 := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	e2.SessionID = "s1"
	e2.StepID = "st1"
	e2.StepIndex = 0
	e2.StepName = "think"
	s.Merge(e2)

	// step.end
	e3 := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	e3.SessionID = "s1"
	e3.StepID = "st1"
	e3.StepIndex = 0
	e3.Model = "gpt-4"
	e3.TokensIn = 100
	e3.TokensOut = 50
	e3.ToolName = "bash"
	e3.ToolInput = "ls"
	e3.ToolOutput = "file.go"
	e3.LatencyMs = 500
	s.Merge(e3)

	steps := s.GetSteps("s1")
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	st := steps[0]
	if st.Model != "gpt-4" {
		t.Fatalf("want model gpt-4, got %s", st.Model)
	}
	if st.TokensIn != 100 || st.TokensOut != 50 {
		t.Fatalf("want tokens 100/50, got %d/%d", st.TokensIn, st.TokensOut)
	}
	if st.ToolName != "bash" {
		t.Fatalf("want tool bash, got %s", st.ToolName)
	}
	if st.LatencyMs != 500 {
		t.Fatalf("want latency 500, got %d", st.LatencyMs)
	}
	if !st.Started || !st.Ended {
		t.Fatalf("want started=true ended=true, got %v/%v", st.Started, st.Ended)
	}
}

func TestStore_CostRollup(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	// parent session
	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "parent"
	e.AgentName = "orchestrator"
	s.Merge(e)

	// parent step
	e2 := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	e2.SessionID = "parent"
	e2.StepID = "ps1"
	e2.StepIndex = 0
	s.Merge(e2)

	e3 := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	e3.SessionID = "parent"
	e3.StepID = "ps1"
	e3.StepIndex = 0
	e3.TokensIn = 200
	e3.TokensOut = 100
	e3.LatencyMs = 100
	s.Merge(e3)

	// child session triggered by parent step
	e4 := ev(agentobs.EventSessionStart, "r1", now.Add(4*time.Second))
	e4.SessionID = "child"
	e4.ParentSessionID = "parent"
	e4.TriggerStepID = "ps1"
	e4.AgentName = "worker"
	s.Merge(e4)

	// child step
	e5 := ev(agentobs.EventStepStart, "r1", now.Add(5*time.Second))
	e5.SessionID = "child"
	e5.StepID = "cs1"
	e5.StepIndex = 0
	s.Merge(e5)

	e6 := ev(agentobs.EventStepEnd, "r1", now.Add(6*time.Second))
	e6.SessionID = "child"
	e6.StepID = "cs1"
	e6.StepIndex = 0
	e6.TokensIn = 300
	e6.TokensOut = 150
	e6.LatencyMs = 200
	s.Merge(e6)

	// end child
	e7 := ev(agentobs.EventSessionEnd, "r1", now.Add(7*time.Second))
	e7.SessionID = "child"
	e7.Success = true
	s.Merge(e7)

	// end parent
	e8 := ev(agentobs.EventSessionEnd, "r1", now.Add(8*time.Second))
	e8.SessionID = "parent"
	e8.Success = true
	s.Merge(e8)

	parent, _ := s.GetSession("parent")
	if parent.ExclusiveTokensIn != 200 {
		t.Fatalf("want exclusive in 200, got %d", parent.ExclusiveTokensIn)
	}
	if parent.InclusiveTokensIn != 500 {
		t.Fatalf("want inclusive in 500, got %d", parent.InclusiveTokensIn)
	}
	if parent.InclusiveTokensOut != 250 {
		t.Fatalf("want inclusive out 250, got %d", parent.InclusiveTokensOut)
	}

	// run inclusive should match root session
	run, _ := s.GetRun("r1")
	if run.InclusiveTokensIn != 500 {
		t.Fatalf("want run inclusive in 500, got %d", run.InclusiveTokensIn)
	}
	if run.InclusiveTokensOut != 250 {
		t.Fatalf("want run inclusive out 250, got %d", run.InclusiveTokensOut)
	}
}

func TestStore_OutOfOrderStepBackfill(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// step.end arrives BEFORE step.start
	eEnd := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	eEnd.SessionID = "s1"
	eEnd.StepID = "st1"
	eEnd.StepIndex = 0
	eEnd.Model = "gpt-4"
	eEnd.TokensIn = 100
	eEnd.TokensOut = 50
	eEnd.LatencyMs = 500
	s.Merge(eEnd)

	// step.start arrives late
	eStart := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	eStart.SessionID = "s1"
	eStart.StepID = "st1"
	eStart.StepIndex = 0
	eStart.StepName = "think"
	s.Merge(eStart)

	steps := s.GetSteps("s1")
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	st := steps[0]
	if !st.Started {
		t.Fatal("want Started=true after late step.start")
	}
	if st.StepName != "think" {
		t.Fatalf("want StepName=think, got %s", st.StepName)
	}
	if st.OffsetMs <= 0 {
		t.Fatalf("want OffsetMs > 0 after backfill, got %d", st.OffsetMs)
	}
	if st.Model != "gpt-4" {
		t.Fatalf("want Model=gpt-4, got %s", st.Model)
	}
}

func TestStore_GetStepsSortedByIndex(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// insert step 2 before step 0
	for _, idx := range []int{2, 0, 1} {
		eS := ev(agentobs.EventStepStart, "r1", now.Add(time.Duration(2+idx)*time.Second))
		eS.SessionID = "s1"
		eS.StepID = "st" + string(rune('0'+idx))
		eS.StepIndex = idx
		s.Merge(eS)
	}

	steps := s.GetSteps("s1")
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(steps))
	}
	for i, st := range steps {
		if st.StepIndex != i {
			t.Fatalf("step[%d] has index %d, want sorted order", i, st.StepIndex)
		}
	}
}

func TestStore_UnknownSessionReturnsNotFound(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	// step.start on unknown session
	e := ev(agentobs.EventStepStart, "r1", now.Add(time.Second))
	e.SessionID = "nonexistent"
	e.StepID = "st1"
	e.StepIndex = 0
	err := s.Merge(e)
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if !strings.Contains(err.Error(), "SESSION_NOT_FOUND") {
		t.Fatalf("want SESSION_NOT_FOUND, got %v", err)
	}
}

func TestStore_LateRunStartBackfillsTime(t *testing.T) {
	s := New()
	now := time.Now()

	// session.start arrives first, auto-creates run with later timestamp
	e := ev(agentobs.EventSessionStart, "r1", now.Add(5*time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	run, _ := s.GetRun("r1")
	if !run.StartTime.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("auto-created run should have session timestamp")
	}

	// run.start arrives late with earlier timestamp
	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	run, _ = s.GetRun("r1")
	if !run.StartTime.Equal(now) {
		t.Fatalf("want backfilled start time %v, got %v", now, run.StartTime)
	}
}

func TestStore_StepIDMismatchRejected(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// step.start with step_id=A
	eStart := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	eStart.SessionID = "s1"
	eStart.StepID = "step-A"
	eStart.StepIndex = 0
	s.Merge(eStart)

	// step.end with different step_id=B but same step_index=0
	eEnd := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	eEnd.SessionID = "s1"
	eEnd.StepID = "step-B"
	eEnd.StepIndex = 0
	eEnd.LatencyMs = 100
	eEnd.TokensIn = 10
	eEnd.TokensOut = 5
	err := s.Merge(eEnd)
	if err == nil {
		t.Fatal("expected error for step_id mismatch")
	}
	if !strings.Contains(err.Error(), "STEP_ID_MISMATCH") {
		t.Fatalf("want STEP_ID_MISMATCH, got %v", err)
	}
}

func TestStore_AutoCompleteSetsTiming(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// end the only session — should auto-complete the run
	e2 := ev(agentobs.EventSessionEnd, "r1", now.Add(5*time.Second))
	e2.SessionID = "s1"
	e2.Success = true
	s.Merge(e2)

	run, _ := s.GetRun("r1")
	if run.State != agentobs.StateCompleted {
		t.Fatalf("want completed, got %s", run.State)
	}
	if run.EndTime.IsZero() {
		t.Fatal("auto-completed run should have EndTime set")
	}
	if run.DurationMs <= 0 {
		t.Fatalf("auto-completed run should have positive DurationMs, got %d", run.DurationMs)
	}
	// EndTime should match the session's EndTime
	sess, _ := s.GetSession("s1")
	if !run.EndTime.Equal(sess.EndTime) {
		t.Fatalf("run EndTime %v should equal session EndTime %v", run.EndTime, sess.EndTime)
	}
}

func TestStore_EndOnlyStepDerivesOffset(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// only step.end, no step.start — offset should be derived from timestamp - latency
	eEnd := ev(agentobs.EventStepEnd, "r1", now.Add(4*time.Second))
	eEnd.SessionID = "s1"
	eEnd.StepID = "st1"
	eEnd.StepIndex = 0
	eEnd.LatencyMs = 1000 // 1 second latency
	eEnd.TokensIn = 50
	eEnd.TokensOut = 25
	s.Merge(eEnd)

	steps := s.GetSteps("s1")
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	// session started at now+1s, step ended at now+4s with 1s latency
	// so approximate start = now+3s, offset from session start = 2s = 2000ms
	if steps[0].OffsetMs != 2000 {
		t.Fatalf("want derived OffsetMs=2000, got %d", steps[0].OffsetMs)
	}
}

func TestStore_Prune(t *testing.T) {
	s := New()
	old := time.Now().Add(-25 * time.Hour)

	// create old completed run
	s.Merge(ev(agentobs.EventRunStart, "old-run", old))

	e := ev(agentobs.EventSessionStart, "old-run", old.Add(time.Second))
	e.SessionID = "old-sess"
	e.AgentName = "a"
	s.Merge(e)

	e2 := ev(agentobs.EventSessionEnd, "old-run", old.Add(2*time.Second))
	e2.SessionID = "old-sess"
	e2.Success = true
	s.Merge(e2)

	e3 := ev(agentobs.EventRunEnd, "old-run", old.Add(3*time.Second))
	e3.Success = true
	s.Merge(e3)

	if s.RunCount() != 1 {
		t.Fatalf("want 1 run before prune, got %d", s.RunCount())
	}

	s.PruneOlderThan(24 * time.Hour)

	if s.RunCount() != 0 {
		t.Fatalf("want 0 runs after prune, got %d", s.RunCount())
	}
	if s.SessionCount() != 0 {
		t.Fatalf("want 0 sessions after prune, got %d", s.SessionCount())
	}
}
