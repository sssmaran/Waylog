package waylogv2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type lifecycleKind uint8

const (
	lifecycleNormal lifecycleKind = iota
	lifecyclePanic
	lifecycleAborted
	lifecycleTimeout
)

// Finalize seals the request, assembles the v2.0 wide event, runs the
// optional Redactor, writes the JSON line to Output, and removes the request
// from the active set. Subsequent calls on the same context return (nil, nil)
// and increment LateCompletionAfterEmit.
//
// Intended for middleware and adapter authors.
func Finalize(ctx context.Context) (*eventv2.Event, error) {
	return finalize(ctx, lifecycleNormal)
}

// FinalizePanic seals the request as a panic-owned lifecycle emit.
func FinalizePanic(ctx context.Context) (*eventv2.Event, error) {
	return finalize(ctx, lifecyclePanic)
}

// FinalizeAborted seals the request as an aborted lifecycle emit unless the
// request had already failed explicitly, in which case the existing error wins.
func FinalizeAborted(ctx context.Context) (*eventv2.Event, error) {
	return finalize(ctx, lifecycleAborted)
}

// FinalizeTimeout seals the request as a timeout-owned lifecycle emit.
func FinalizeTimeout(ctx context.Context) (*eventv2.Event, error) {
	return finalize(ctx, lifecycleTimeout)
}

func finalize(ctx context.Context, lifecycle lifecycleKind) (*eventv2.Event, error) {
	r := requestFromContext(ctx)
	if r == nil {
		return nil, ErrNoActiveRequest
	}

	r.mu.Lock()
	if r.sealed {
		r.mu.Unlock()
		r.sdk.lateAfterEmit.Add(1)
		return nil, nil
	}
	r.applyLifecycleLocked(lifecycle)
	r.sealed = true
	now := time.Now().UTC()
	ev := r.assembleLocked(now)
	r.mu.Unlock()

	r.sdk.mu.Lock()
	delete(r.sdk.active, r)
	r.sdk.mu.Unlock()

	if r.sdk.cfg.Redactor != nil && len(ev.Fields) > 0 {
		ev.Fields = r.sdk.cfg.Redactor(ev.Fields)
	}

	accepted, err := deliver(r.sdk, ev)
	if err != nil {
		return ev, err
	}
	if accepted {
		r.sdk.emitted.Add(1)
	}
	if accepted && ev.Status == eventv2.StatusSuppressed {
		r.sdk.suppressed.Add(1)
	}
	r.sdk.emitDevFinal(ev)
	return ev, nil
}

func (r *request) applyLifecycleLocked(lifecycle lifecycleKind) {
	if r.suppressed {
		return
	}

	now := time.Now()
	switch lifecycle {
	case lifecyclePanic:
		// Panic is an app-observable failure, so it synthesizes both an
		// anchor and an entry in errors[] (§3.4: errors[] is the deduped
		// list of errors seen across the request).
		if r.anchorStep == "" || r.anchorFromStepPanic {
			r.markLifecycleLocked(eventv2.StatusError, eventv2.CodePanic)
		} else {
			r.finalStatus = eventv2.StatusError
		}
		r.recordErrorLocked(eventv2.CodePanic, "runtime panic recovered")
		r.flushActiveStepsLocked(now)
	case lifecycleTimeout:
		// Watchdog expiry is a runtime decision, not an app error; the
		// lifecycle code lives only in anchor, not errors[].
		if r.anchorStep == "" {
			r.markLifecycleLocked(eventv2.StatusTimeout, eventv2.CodeTimeout)
		} else {
			r.finalStatus = eventv2.StatusTimeout
		}
		r.flushActiveStepsLocked(now)
	case lifecycleAborted:
		// Cooperative cancel from the client; same errors[] rule as timeout.
		// Preserve any failure anchor that was recorded before cancel.
		if r.anchorStep == "" {
			r.markLifecycleLocked(eventv2.StatusAborted, eventv2.CodeAborted)
		}
		r.flushActiveStepsLocked(now)
	}
}

// flushActiveStepsLocked snapshots any steps still on the active stack as
// closed steps (status=ok) so a watchdog/panic/cancel that fires while a
// step is in flight still records that the step ran. Spec §3.5: when
// lifecycle precedence wins the seal, the active step at fire time is
// captured both as the anchor and as a step entry.
func (r *request) flushActiveStepsLocked(now time.Time) {
	if len(r.stepStack) == 0 {
		return
	}
	for _, active := range r.stepStack {
		durMS := int64(now.Sub(active.startedAt) / 1e6)
		if durMS < 0 {
			durMS = 0
		}
		r.addStepLocked(stepBuf{
			name:       active.name,
			spanID:     active.spanID,
			startMS:    active.startMS,
			durationMS: durMS,
			status:     stepStatusOK,
			downstream: active.downstream,
		})
	}
	r.stepStack = nil
}

func (r *request) assembleLocked(tsEnd time.Time) *eventv2.Event {
	ev := &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       r.eventID,
		TsStart:       r.tsStart,
		TsEnd:         tsEnd,
		DurationMS:    int64(tsEnd.Sub(r.tsStart) / 1e6),
		Kind:          "http",
		Service:       r.sdk.cfg.Service,
		Env:           r.sdk.cfg.Env,
		Version:       r.sdk.cfg.Version,
		TraceID:       r.traceID,
		SpanID:        r.spanID,
		ParentSpanID:  r.parentSpanID,
		Status:        r.snapshotStatusLocked(),
	}

	if len(r.fields) > 0 {
		if r.sdk.cfg.Redactor != nil {
			ev.Fields = make(map[string]any, len(r.fields))
			maps.Copy(ev.Fields, r.fields)
		} else {
			ev.Fields = r.fields
			r.fields = nil
		}
	}

	// Suppressed events keep top-level identity, timing, and fields (§4.2.2)
	// but drop anchor/steps/logs/errors so they remain header-only triage.
	if ev.Status == eventv2.StatusSuppressed {
		return ev
	}

	if r.anchorStep != "" && r.anchorCode != "" {
		ev.Anchor = &eventv2.Anchor{Step: r.anchorStep, ErrorCode: r.anchorCode}
	}

	if r.headerOnly {
		// Header-heavy fallback per §4.4: identity + status + anchor + fields,
		// no steps[]/logs[]/errors[].
		return ev
	}

	if len(r.steps) > 0 {
		ev.Steps = make([]eventv2.Step, 0, len(r.steps))
		for _, s := range r.steps {
			step := eventv2.Step{
				Name:       s.name,
				SpanID:     s.spanID,
				StartMS:    s.startMS,
				DurationMS: s.durationMS,
				Status:     s.status,
				Downstream: s.downstream,
			}
			if s.err != nil {
				step.Error = s.err.toStepError()
			}
			ev.Steps = append(ev.Steps, step)
		}
	}

	if len(r.logs) > 0 {
		ev.Logs = make([]eventv2.Log, 0, len(r.logs))
		for _, l := range r.logs {
			ev.Logs = append(ev.Logs, eventv2.Log{
				TsOffsetMS: l.tsOffsetMS,
				Level:      l.level,
				Msg:        l.msg,
				Fields:     l.fields,
			})
		}
	}

	if len(r.errs) > 0 {
		ev.Errors = make([]eventv2.ErrorRef, 0, len(r.errs))
		for _, e := range r.errs {
			ev.Errors = append(ev.Errors, eventv2.ErrorRef{Code: e.code, Reason: e.reason})
		}
	}

	return ev
}

// snapshotStatusLocked computes the current status without sealing. Caller
// must hold r.mu.
func (r *request) snapshotStatusLocked() eventv2.Status {
	if r.suppressed {
		return eventv2.StatusSuppressed
	}
	if r.finalStatus != "" {
		return r.finalStatus
	}
	if r.anchorStep != "" {
		return eventv2.StatusError
	}
	return eventv2.StatusOK
}

func emit(w io.Writer, ev *eventv2.Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("waylog: marshal event: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("waylog: write event: %w", err)
	}
	return nil
}

func deliver(s *sdk, ev *eventv2.Event) (bool, error) {
	if !s.allowEvent(ev) {
		s.eventsDropped.Add(1)
		return false, nil
	}
	if s.delivery != nil {
		return s.delivery.Submit(ev), nil
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if err := emit(s.out, ev); err != nil {
		return false, err
	}
	return true, nil
}

func (s *sdk) allowEvent(ev *eventv2.Event) bool {
	if s.cfg.MaxEventsPerSec <= 0 || ev == nil || ev.Status.IsPriority() {
		return true
	}

	now := time.Now().Unix()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateSecond != now {
		s.rateSecond = now
		s.rateCount = 0
	}
	if s.rateCount >= s.cfg.MaxEventsPerSec {
		return false
	}
	s.rateCount++
	return true
}
