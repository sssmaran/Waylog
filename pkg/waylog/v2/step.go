package waylogv2

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Step runs fn as a named span within the active request. The returned T and
// error are passed through verbatim. If fn returns an error, the step records
// it and (on first failure) becomes the request anchor.
//
// If ctx has no active Waylog request, fn runs and Step is a thin pass-through.
// If name is empty, fn runs without opening a step (the empty name would
// collide with the "no anchor yet" sentinel).
func Step[T any](ctx context.Context, name string, fn func(ctx context.Context) (T, error)) (v T, err error) {
	r := requestFromContext(ctx)
	if r == nil || name == "" {
		return fn(ctx)
	}
	startedAt := time.Now()
	startMS := int64(startedAt.Sub(r.tsStart) / 1e6)
	r.pushStep(name, startedAt, startMS)
	defer func() {
		dur := int64(time.Since(startedAt) / 1e6)
		if rec := recover(); rec != nil {
			r.rememberPanicStep(name)
			r.closeStep(name, startMS, dur, fmt.Errorf("panic: %v", rec))
			panic(rec)
		}
		r.closeStep(name, startMS, dur, err)
	}()
	v, err = fn(ctx)
	return v, err
}

// StepVoid is the no-return-value form of Step.
func StepVoid(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	_, err := Step[struct{}](ctx, name, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

// Fail explicitly marks the request as failed with the given Error. It does
// not abort the handler. Multiple Fail calls within one request are allowed;
// the first one wins for anchor purposes.
//
// Reserved WAYLOG_* codes are rejected (counted in
// StatsSnapshot.ReservedCodeRejections); the call is otherwise a no-op.
//
// On a suppressed request, Fail increments the suppressed_then_failed counter
// for visibility but does not change the emitted event.
func Fail(ctx context.Context, err *Error) {
	r := requestFromContext(ctx)
	if r == nil || err == nil || err.Code == "" {
		return
	}
	if isReserved(err.Code) {
		recordReservedRejection(err.Code, "Fail")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed {
		r.sdk.suppressFailed.Add(1)
		return
	}
	if r.sealed {
		return
	}
	r.recordErrorLocked(err.Code, err.Reason)
	if r.anchorStep == "" {
		if n := len(r.stepStack); n > 0 {
			r.anchorStep = r.stepStack[n-1].name
		} else {
			r.anchorStep = "request"
		}
		r.anchorCode = err.Code
		r.anchorFromStepPanic = false
	}
}

// Suppress marks the request as excluded from detailed buffering. Suppress is
// idempotent and non-reversible within a single request. Subsequent Fail
// calls only increment the suppressed_then_failed counter.
func Suppress(ctx context.Context) {
	r := requestFromContext(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed {
		return
	}
	r.suppressed = true
	r.steps = nil
	r.logs = nil
	r.errs = nil
	r.bufBytes = 0
	r.headerOnly = false
	r.finalStatus = ""
	r.anchorStep = ""
	r.anchorCode = ""
	r.anchorFromStepPanic = false
	r.panicStepHint = ""
}

func (r *request) rememberPanicStep(name string) {
	r.mu.Lock()
	if r.panicStepHint == "" {
		r.panicStepHint = name
	}
	r.mu.Unlock()
}

func (r *request) pushStep(name string, startedAt time.Time, startMS int64) {
	r.mu.Lock()
	r.stepStack = append(r.stepStack, activeStep{
		name:      name,
		startedAt: startedAt,
		startMS:   startMS,
	})
	r.mu.Unlock()
}

func (r *request) closeStep(name string, startMS, durationMS int64, fnErr error) {
	status := stepStatusOK
	var werr *Error
	if fnErr != nil {
		status = stepStatusError
		werr = &Error{Code: "ERR", Reason: fnErr.Error()}
		var asWaylog *Error
		if errors.As(fnErr, &asWaylog) {
			if isReserved(asWaylog.Code) {
				recordReservedRejection(asWaylog.Code, "Step")
			} else {
				werr = asWaylog
			}
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	active := r.popStackLocked(name)
	if r.suppressed || r.sealed {
		return
	}
	r.addStepLocked(stepBuf{
		name:       name,
		spanID:     active.spanID,
		startMS:    startMS,
		durationMS: durationMS,
		status:     status,
		downstream: active.downstream,
		err:        werr,
	})
}

func (r *request) popStackLocked(name string) activeStep {
	if n := len(r.stepStack); n > 0 && r.stepStack[n-1].name == name {
		top := r.stepStack[n-1]
		r.stepStack = r.stepStack[:n-1]
		return top
	}
	for i := len(r.stepStack) - 1; i >= 0; i-- {
		if r.stepStack[i].name == name {
			top := r.stepStack[i]
			r.stepStack = append(r.stepStack[:i], r.stepStack[i+1:]...)
			return top
		}
	}
	return activeStep{name: name}
}
