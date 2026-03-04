package tools

import (
	"errors"
	"fmt"
	"testing"
)

func TestToolError_Error(t *testing.T) {
	te := &ToolError{Code: CodeInvalidParams, Message: "bad input"}
	want := "[INVALID_PARAMS] bad input"
	if got := te.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestToolError_Implements(t *testing.T) {
	var err error = &ToolError{Code: CodeInternal, Message: "boom"}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestAsToolError_Match(t *testing.T) {
	orig := &ToolError{Code: CodeNotFound, Message: "missing", Retryable: false}
	wrapped := fmt.Errorf("wrap: %w", orig)
	te, ok := AsToolError(wrapped)
	if !ok {
		t.Fatal("expected match")
	}
	if te.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", te.Code, CodeNotFound)
	}
}

func TestAsToolError_NoMatch(t *testing.T) {
	_, ok := AsToolError(errors.New("plain error"))
	if ok {
		t.Fatal("expected no match")
	}
}

func TestAsToolError_Nil(t *testing.T) {
	_, ok := AsToolError(nil)
	if ok {
		t.Fatal("expected no match for nil")
	}
}
