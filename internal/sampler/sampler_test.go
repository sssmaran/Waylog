package sampler

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func TestShouldKeep_AlwaysKeepErrors(t *testing.T) {
	s := New(Config{HappySampleRatePct: 0})
	ev := event.WideEvent{Outcome: event.OutcomeContext{Success: false}}
	if !s.ShouldKeep(ev) {
		t.Error("expected errors to always be kept")
	}
}

func TestShouldKeep_AlwaysKeepSlow(t *testing.T) {
	s := New(Config{SlowMs: 100, HappySampleRatePct: 0})
	ev := event.WideEvent{
		Outcome: event.OutcomeContext{Success: true},
		Metrics: event.MetricsContext{LatencyMs: 200},
	}
	if !s.ShouldKeep(ev) {
		t.Error("expected slow requests to always be kept")
	}
}

func TestShouldKeep_AlwaysKeepVIP(t *testing.T) {
	s := New(Config{HappySampleRatePct: 0})
	ev := event.WideEvent{
		Outcome: event.OutcomeContext{Success: true},
		User:    event.UserContext{VIP: true},
	}
	if !s.ShouldKeep(ev) {
		t.Error("expected VIP requests to always be kept")
	}
}

func TestSameTraceID_SameDecision(t *testing.T) {
	s := New(Config{HappySampleRatePct: 50})
	traceID := "aaaa0000bbbb1111cccc2222dddd3333"

	// Two spans from the same trace with different services/event names
	ev1 := event.WideEvent{
		Outcome:   event.OutcomeContext{Success: true},
		Request:   event.RequestContext{TraceID: traceID},
		EventName: "gateway.request",
		System:    event.SystemContext{Service: "gateway"},
		User:      event.UserContext{ID: "user-1"},
	}
	ev2 := event.WideEvent{
		Outcome:   event.OutcomeContext{Success: true},
		Request:   event.RequestContext{TraceID: traceID},
		EventName: "payment.request",
		System:    event.SystemContext{Service: "payment"},
		User:      event.UserContext{ID: "user-2"},
	}

	d1 := s.ShouldKeep(ev1)
	d2 := s.ShouldKeep(ev2)
	if d1 != d2 {
		t.Errorf("same trace_id got different decisions: ev1=%v ev2=%v", d1, d2)
	}
}

func TestDifferentTraceID_CanDiffer(t *testing.T) {
	// With 50% sampling, different trace IDs should produce different decisions
	// for at least some pairs.
	s := New(Config{HappySampleRatePct: 50})
	kept := 0
	total := 100
	for i := 0; i < total; i++ {
		traceID := padHex(i)
		ev := event.WideEvent{
			Outcome: event.OutcomeContext{Success: true},
			Request: event.RequestContext{TraceID: traceID},
		}
		if s.ShouldKeep(ev) {
			kept++
		}
	}
	// At 50% rate, expect roughly 30-70 kept out of 100.
	if kept == 0 || kept == total {
		t.Errorf("expected variation in sampling, got kept=%d/%d", kept, total)
	}
}

func padHex(n int) string {
	s := make([]byte, 32)
	for i := range s {
		s[i] = '0'
	}
	hex := []byte("0123456789abcdef")
	i := len(s) - 1
	for v := n; v > 0 && i >= 0; v /= 16 {
		s[i] = hex[v%16]
		i--
	}
	return string(s)
}
