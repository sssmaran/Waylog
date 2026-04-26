package waylogv2

import (
	"context"
	"maps"
	"time"
)

// Logger is the request-scoped logging surface returned by From.
type Logger interface {
	Info(msg string, fields ...F)
	Warn(msg string, fields ...F)
	Error(msg string, err *Error, fields ...F)
}

// From returns a request-scoped Logger. If ctx has no active Waylog request,
// the returned Logger is a no-op.
func From(ctx context.Context) Logger {
	r := requestFromContext(ctx)
	if r == nil {
		return noopLogger{}
	}
	return &reqLogger{r: r}
}

type reqLogger struct{ r *request }

func (l *reqLogger) Info(msg string, fields ...F) { l.r.appendLog("info", msg, nil, fields) }
func (l *reqLogger) Warn(msg string, fields ...F) { l.r.appendLog("warn", msg, nil, fields) }
func (l *reqLogger) Error(msg string, err *Error, fields ...F) {
	l.r.appendLog("error", msg, err, fields)
}

type noopLogger struct{}

func (noopLogger) Info(string, ...F)          {}
func (noopLogger) Warn(string, ...F)          {}
func (noopLogger) Error(string, *Error, ...F) {}

func (r *request) appendLog(level, msg string, err *Error, more []F) {
	if r == nil {
		return
	}

	merged := mergeFields(more)
	if err != nil {
		if merged == nil {
			merged = F{}
		}
		merged["error.code"] = err.Code
		if err.Reason != "" {
			merged["error.reason"] = err.Reason
		}
		if err.Cause != "" {
			merged["error.cause"] = err.Cause
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed || r.sealed {
		return
	}

	stepName := ""
	if n := len(r.stepStack); n > 0 {
		stepName = r.stepStack[n-1]
	}

	if err != nil {
		r.recordErrorLocked(err.Code, err.Reason)
	}

	r.addLogLocked(logBuf{
		tsOffsetMS: int64(time.Since(r.tsStart) / 1e6),
		level:      level,
		msg:        msg,
		fields:     merged,
		stepName:   stepName,
	})
}

// mergeFields returns a freshly-allocated shallow copy of the merged maps.
// Mutating the *outer* input F after the call is safe; **nested** map/slice
// values inside the F remain caller-owned and MAY mutate the buffered log
// entry if the caller mutates them afterward.
//
// This is an intentional split from SetField (which deep-clones): logger
// calls are on the per-request hot path where deep clone cost compounds.
// Snapshot-at-call lives in SetField; the logger contract is "don't mutate
// nested objects after passing them to Info/Warn/Error".
//
// Empty inputs return nil to avoid emitting `"fields":{}`.
func mergeFields(in []F) F {
	if len(in) == 0 {
		return nil
	}
	out := F{}
	for _, m := range in {
		maps.Copy(out, m)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
