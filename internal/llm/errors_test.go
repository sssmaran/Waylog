package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestProviderError_Error(t *testing.T) {
	pe := &ProviderError{Provider: "gemini", StatusCode: 429, Message: "rate limited"}
	got := pe.Error()
	want := "gemini: rate limited (status 429)"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestProviderError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("connection reset")
	pe := &ProviderError{Provider: "gemini", Cause: cause}
	if !errors.Is(pe, cause) {
		t.Error("expected Unwrap to expose cause")
	}
}

func TestProviderError_ErrorsAs(t *testing.T) {
	pe := &ProviderError{Provider: "gemini", StatusCode: 500}
	wrapped := fmt.Errorf("ask failed: %w", pe)
	var target *ProviderError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As failed")
	}
	if target.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", target.StatusCode)
	}
}
