package waylogv2

import (
	"fmt"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

// Error is the structured failure value carried through Fail.
type Error struct {
	Code   string
	Reason string
	Cause  string
}

// Opt configures a NewError.
type Opt func(*Error)

func WithReason(reason string) Opt { return func(e *Error) { e.Reason = reason } }
func WithCause(cause string) Opt   { return func(e *Error) { e.Cause = cause } }

// NewError builds a structured Error. Reserved WAYLOG_* codes are rejected
// (returns nil) and counted in StatsSnapshot.ReservedCodeRejections so misuse
// is observable. Lifecycle codes must come from SDK synthesis.
func NewError(code string, opts ...Opt) *Error {
	if isReserved(code) {
		recordReservedRejection(code, "NewError")
		return nil
	}
	e := &Error{Code: code}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Reason)
	}
	return e.Code
}

func isReserved(code string) bool {
	switch code {
	case eventv2.CodeTimeout, eventv2.CodeAborted, eventv2.CodePanic, eventv2.CodePartial:
		return true
	}
	return false
}

func recordReservedRejection(code, where string) {
	s := getState()
	if s == nil {
		return
	}
	s.reservedRejected.Add(1)
	if s.devEnabled && s.devOut != nil {
		s.devMu.Lock()
		defer s.devMu.Unlock()
		fmt.Fprintf(s.devOut, "waylog: %s rejected reserved error code %q\n", where, code)
	}
}

func (e *Error) toStepError() *eventv2.StepError {
	if e == nil {
		return nil
	}
	return &eventv2.StepError{Code: e.Code, Reason: e.Reason, Cause: e.Cause}
}
