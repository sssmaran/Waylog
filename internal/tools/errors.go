package tools

import (
	"errors"
	"fmt"
)

// ToolError codes.
const (
	CodeInvalidParams = "INVALID_PARAMS"
	CodeNotFound      = "NOT_FOUND"
	CodeEmptyResult   = "EMPTY_RESULT"
	CodeTimeout       = "TIMEOUT"
	CodeInternal      = "INTERNAL"
	CodeGraphEmpty    = "GRAPH_EMPTY"
)

// ToolError is a structured error returned by tool handlers.
type ToolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// AsToolError extracts a *ToolError from an error chain.
func AsToolError(err error) (*ToolError, bool) {
	var te *ToolError
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}
