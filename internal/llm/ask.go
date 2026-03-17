package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ToolExecutor interface {
	Call(ctx context.Context, name string, params json.RawMessage) (any, error)
}

type ToolExecutorFunc func(ctx context.Context, name string, params json.RawMessage) (any, error)

func (f ToolExecutorFunc) Call(ctx context.Context, name string, params json.RawMessage) (any, error) {
	return f(ctx, name, params)
}

// AskOptions controls Ask behavior.
type AskOptions struct {
	MaxSteps      int
	ErrorStrategy string // "continue" or "abort" (default)
}

// ToolCallRecord captures a single tool invocation during Ask.
type ToolCallRecord struct {
	Name       string          `json:"name"`
	Params     json.RawMessage `json:"params"`
	Result     any             `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMs int64           `json:"duration_ms"`
}

func Ask(ctx context.Context, provider Provider, tools []ToolDefinition, exec ToolExecutor, prompt string, opts AskOptions) (string, []ToolCallRecord, error) {
	if provider == nil {
		return "", nil, fmt.Errorf("llm provider required")
	}
	if exec == nil {
		return "", nil, fmt.Errorf("tool executor required")
	}
	if prompt == "" {
		return "", nil, fmt.Errorf("prompt required")
	}
	maxSteps := opts.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 5
	}
	strategy := opts.ErrorStrategy
	if strategy == "" {
		strategy = "abort"
	}

	history := make([]Turn, 0, maxSteps*2)
	var records []ToolCallRecord

	for i := 0; i < maxSteps; i++ {
		res, err := provider.Generate(ctx, prompt, tools, history)
		if err != nil {
			return "", records, err
		}
		if len(res.ToolCalls) == 0 {
			if res.Text == "" {
				return "", records, fmt.Errorf("llm returned empty response")
			}
			return res.Text, records, nil
		}

		for _, call := range res.ToolCalls {
			history = append(history, Turn{
				Role:     "assistant",
				ToolCall: &call,
			})

			start := time.Now()
			result, callErr := exec.Call(ctx, call.Name, call.Arguments)
			rec := ToolCallRecord{
				Name:       call.Name,
				Params:     call.Arguments,
				DurationMs: time.Since(start).Milliseconds(),
			}

			if callErr != nil {
				rec.Error = callErr.Error()
				records = append(records, rec)

				if strategy == "continue" {
					// Feed error as tool result so LLM can adapt
					history = append(history, Turn{
						Role: "tool",
						ToolResult: &ToolResult{
							Name:   call.Name,
							Result: map[string]string{"error": callErr.Error()},
						},
					})
					continue
				}
				return "", records, callErr
			}

			rec.Result = result
			records = append(records, rec)
			history = append(history, Turn{
				Role: "tool",
				ToolResult: &ToolResult{
					Name:   call.Name,
					Result: result,
				},
			})
		}
	}

	return "", records, fmt.Errorf("tool calling exceeded max steps")
}
