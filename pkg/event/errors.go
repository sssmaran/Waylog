package event

import "strings"

// ValidationErrors collects field-level validation failures.
// Implements the error interface — external callers see the same
// string output as before. Internal callers can type-assert to
// inspect individual field errors.
type ValidationErrors []ValidationError

// ValidationError represents a single field validation failure.
type ValidationError struct {
	Field   string // e.g., "user.id", "trace_id", "schema_version"
	Message string
}

func (ve ValidationErrors) Error() string {
	if len(ve) == 1 {
		return ve[0].Message
	}
	msgs := make([]string, len(ve))
	for i, e := range ve {
		msgs[i] = e.Message
	}
	return strings.Join(msgs, "; ")
}

// HasOnly returns true if all errors are for the given field.
func (ve ValidationErrors) HasOnly(field string) bool {
	if len(ve) == 0 {
		return false
	}
	for _, e := range ve {
		if e.Field != field {
			return false
		}
	}
	return true
}
