package store

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/agentobs"
)

func TestListRuns_OrderAndPagination(t *testing.T) {
	s := New()
	now := time.Now()

	for i, id := range []string{"r1", "r2", "r3"} {
		e := ev(agentobs.EventRunStart, id, now.Add(time.Duration(i+1)*time.Second))
		s.Merge(e)
	}

	// all runs, DESC order
	all := s.ListRuns(0, "", time.Time{})
	if len(all) != 3 {
		t.Fatalf("want 3 runs, got %d", len(all))
	}
	if all[0].RunID != "r3" || all[1].RunID != "r2" || all[2].RunID != "r1" {
		t.Fatalf("want DESC order r3,r2,r1, got %s,%s,%s", all[0].RunID, all[1].RunID, all[2].RunID)
	}

	// limit=2
	limited := s.ListRuns(2, "", time.Time{})
	if len(limited) != 2 {
		t.Fatalf("want 2 runs with limit=2, got %d", len(limited))
	}

	// before=T+2s should return only r1 (StartTime < T+2s)
	before := s.ListRuns(0, "", now.Add(2*time.Second))
	if len(before) != 1 {
		t.Fatalf("want 1 run before T+2s, got %d", len(before))
	}
	if before[0].RunID != "r1" {
		t.Fatalf("want r1, got %s", before[0].RunID)
	}
}

func TestListRuns_StatusFilter(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))
	s.Merge(ev(agentobs.EventRunStart, "r2", now.Add(time.Second)))

	// complete r1
	e := ev(agentobs.EventRunEnd, "r1", now.Add(2*time.Second))
	e.Success = true
	s.Merge(e)

	completed := s.ListRuns(0, agentobs.StateCompleted, time.Time{})
	if len(completed) != 1 {
		t.Fatalf("want 1 completed run, got %d", len(completed))
	}
	if completed[0].RunID != "r1" {
		t.Fatalf("want r1, got %s", completed[0].RunID)
	}
}

func TestGetRunSessions(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e1 := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e1.SessionID = "s1"
	e1.AgentName = "a"
	s.Merge(e1)

	e2 := ev(agentobs.EventSessionStart, "r1", now.Add(2*time.Second))
	e2.SessionID = "s2"
	e2.AgentName = "b"
	s.Merge(e2)

	sessions := s.GetRunSessions("r1")
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}

	ids := map[string]bool{}
	for _, sess := range sessions {
		ids[sess.SessionID] = true
	}
	if !ids["s1"] || !ids["s2"] {
		t.Fatalf("want s1 and s2, got %v", ids)
	}
}

func TestScanAbandoned(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now.Add(-10*time.Minute)))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(-10*time.Minute))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// LastEventAt is set by session.start, which is 10min ago
	s.ScanAbandoned(1 * time.Minute)

	sess, _ := s.GetSession("s1")
	if sess.State != agentobs.StateAbandoned {
		t.Fatalf("want abandoned, got %s", sess.State)
	}

	run, _ := s.GetRun("r1")
	if run.State != agentobs.StateAbandoned {
		t.Fatalf("want run abandoned, got %s", run.State)
	}
}

func TestAggregateStats(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	eStep := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	eStep.SessionID = "s1"
	eStep.StepID = "st1"
	eStep.StepIndex = 0
	s.Merge(eStep)

	eEnd := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	eEnd.SessionID = "s1"
	eEnd.StepID = "st1"
	eEnd.StepIndex = 0
	eEnd.TokensIn = 100
	eEnd.TokensOut = 50
	eEnd.LatencyMs = 500
	s.Merge(eEnd)

	st := s.AggregateStats(1 * time.Hour)
	if st.RunCount != 1 {
		t.Fatalf("want 1 run, got %d", st.RunCount)
	}
	if st.SessionCount != 1 {
		t.Fatalf("want 1 session, got %d", st.SessionCount)
	}
	if st.StepCount != 1 {
		t.Fatalf("want 1 step, got %d", st.StepCount)
	}
	if st.AvgTokensIn != 100 {
		t.Fatalf("want avg tokens in 100, got %f", st.AvgTokensIn)
	}
	if st.AvgTokensOut != 50 {
		t.Fatalf("want avg tokens out 50, got %f", st.AvgTokensOut)
	}
}

func TestToolAnalytics(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// bash success
	eS1 := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	eS1.SessionID = "s1"
	eS1.StepID = "st1"
	eS1.StepIndex = 0
	s.Merge(eS1)

	eE1 := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	eE1.SessionID = "s1"
	eE1.StepID = "st1"
	eE1.StepIndex = 0
	eE1.ToolName = "bash"
	eE1.LatencyMs = 100
	s.Merge(eE1)

	// bash error
	eS2 := ev(agentobs.EventStepStart, "r1", now.Add(4*time.Second))
	eS2.SessionID = "s1"
	eS2.StepID = "st2"
	eS2.StepIndex = 1
	s.Merge(eS2)

	eE2 := ev(agentobs.EventStepEnd, "r1", now.Add(5*time.Second))
	eE2.SessionID = "s1"
	eE2.StepID = "st2"
	eE2.StepIndex = 1
	eE2.ToolName = "bash"
	eE2.ToolError = "exit 1"
	eE2.LatencyMs = 200
	s.Merge(eE2)

	analytics := s.ToolAnalytics(1 * time.Hour)
	if len(analytics) != 1 {
		t.Fatalf("want 1 tool, got %d", len(analytics))
	}
	ts := analytics[0]
	if ts.ToolName != "bash" {
		t.Fatalf("want bash, got %s", ts.ToolName)
	}
	if ts.CallCount != 2 {
		t.Fatalf("want 2 calls, got %d", ts.CallCount)
	}
	if ts.SuccessCount != 1 {
		t.Fatalf("want 1 success, got %d", ts.SuccessCount)
	}
	if ts.SuccessRate != 0.5 {
		t.Fatalf("want 0.5 success rate, got %f", ts.SuccessRate)
	}
}

func TestTokensByModel(t *testing.T) {
	s := New()
	now := time.Now()

	s.Merge(ev(agentobs.EventRunStart, "r1", now))

	e := ev(agentobs.EventSessionStart, "r1", now.Add(time.Second))
	e.SessionID = "s1"
	e.AgentName = "a"
	s.Merge(e)

	// step 1
	eS1 := ev(agentobs.EventStepStart, "r1", now.Add(2*time.Second))
	eS1.SessionID = "s1"
	eS1.StepID = "st1"
	eS1.StepIndex = 0
	s.Merge(eS1)

	eE1 := ev(agentobs.EventStepEnd, "r1", now.Add(3*time.Second))
	eE1.SessionID = "s1"
	eE1.StepID = "st1"
	eE1.StepIndex = 0
	eE1.Model = "gpt-4"
	eE1.TokensIn = 100
	eE1.TokensOut = 50
	eE1.LatencyMs = 500
	s.Merge(eE1)

	// step 2 same model
	eS2 := ev(agentobs.EventStepStart, "r1", now.Add(4*time.Second))
	eS2.SessionID = "s1"
	eS2.StepID = "st2"
	eS2.StepIndex = 1
	s.Merge(eS2)

	eE2 := ev(agentobs.EventStepEnd, "r1", now.Add(5*time.Second))
	eE2.SessionID = "s1"
	eE2.StepID = "st2"
	eE2.StepIndex = 1
	eE2.Model = "gpt-4"
	eE2.TokensIn = 200
	eE2.TokensOut = 100
	eE2.LatencyMs = 300
	s.Merge(eE2)

	result := s.TokensByModel(1 * time.Hour)
	tc, ok := result["gpt-4"]
	if !ok {
		t.Fatal("want gpt-4 in result")
	}
	if tc.TokensIn != 300 {
		t.Fatalf("want 300 tokens in, got %d", tc.TokensIn)
	}
	if tc.TokensOut != 150 {
		t.Fatalf("want 150 tokens out, got %d", tc.TokensOut)
	}
}
