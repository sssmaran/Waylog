package llm

import "fmt"

// ProviderError represents a failure from an LLM provider (API error, transport failure).
type ProviderError struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Message    string
	Cause      error
}

func (e *ProviderError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: %s (status %d)", e.Provider, e.Message, e.StatusCode)
	}
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Cause }
