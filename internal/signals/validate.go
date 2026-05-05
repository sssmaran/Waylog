package signals

import (
	"fmt"
	"strings"
	"time"
)

const (
	CodeInvalidField            = "invalid_field"
	CodeUnknownType             = "unknown_type"
	CodeUnknownSeverity         = "unknown_severity"
	CodeTimestampTooFarInFuture = "timestamp_too_far_in_future"
	CodeBodyOversize            = "body_oversize"
	CodeInvalidBody             = "invalid_body"
	CodeInvalidJSON             = "invalid_json"
	CodeUnsupportedMethod       = "unsupported_method"
	CodeDurabilityUnavailable   = "durability_unavailable"
	CodeInternalError           = "internal_error"
)

type ValidationError struct {
	Code   string
	Field  string
	Detail string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Detail
	}
	return e.Field + ": " + e.Detail
}

func Validate(s *Signal, now time.Time, futureSkew time.Duration) error {
	if s == nil {
		return invalidField("signal", "signal is required")
	}
	if strings.TrimSpace(string(s.Type)) == "" {
		return invalidField("type", "type is required")
	}
	if !s.Type.Valid() {
		return &ValidationError{Code: CodeUnknownType, Field: "type", Detail: fmt.Sprintf("unknown type %q", s.Type)}
	}
	if strings.TrimSpace(s.Source) == "" {
		return invalidField("source", "source is required")
	}
	if strings.TrimSpace(s.Service) == "" {
		return invalidField("service", "service is required")
	}
	if strings.TrimSpace(s.Env) == "" {
		return invalidField("env", "env is required")
	}
	if strings.TrimSpace(string(s.Severity)) == "" {
		return invalidField("severity", "severity is required")
	}
	if !s.Severity.Valid() {
		return &ValidationError{Code: CodeUnknownSeverity, Field: "severity", Detail: fmt.Sprintf("unknown severity %q", s.Severity)}
	}
	if strings.TrimSpace(s.Reason) == "" {
		return invalidField("reason", "reason is required")
	}
	if s.Timestamp.IsZero() {
		return invalidField("timestamp", "timestamp is required")
	}
	if futureSkew > 0 && s.Timestamp.After(now.UTC().Add(futureSkew)) {
		return &ValidationError{Code: CodeTimestampTooFarInFuture, Field: "timestamp", Detail: "timestamp is too far in the future"}
	}
	return nil
}

func invalidField(field, detail string) *ValidationError {
	return &ValidationError{Code: CodeInvalidField, Field: field, Detail: detail}
}
