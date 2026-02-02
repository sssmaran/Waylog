package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

type ToolExecutor interface {
	Call(ctx context.Context, name string, params json.RawMessage) (any, error)
}

type ToolExecutorFunc func(ctx context.Context, name string, params json.RawMessage) (any, error)

func (f ToolExecutorFunc) Call(ctx context.Context, name string, params json.RawMessage) (any, error) {
	return f(ctx, name, params)
}

func Ask(ctx context.Context, provider Provider, tools []ToolDefinition, exec ToolExecutor, prompt string, maxSteps int) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("llm provider required")
	}
	if exec == nil {
		return "", fmt.Errorf("tool executor required")
	}
	if prompt == "" {
		return "", fmt.Errorf("prompt required")
	}
	if maxSteps <= 0 {
		maxSteps = 5
	}

	history := make([]Turn, 0, maxSteps*2)

	for i := 0; i < maxSteps; i++ {
		res, err := provider.Generate(ctx, prompt, tools, history)
		if err != nil {
			return "", err
		}
		if len(res.ToolCalls) == 0 {
			if res.Text == "" {
				return "", fmt.Errorf("llm returned empty response")
			}
			return res.Text, nil
		}

		for _, call := range res.ToolCalls {
			history = append(history, Turn{
				Role:     "assistant",
				ToolCall: &call,
			})
			result, err := exec.Call(ctx, call.Name, call.Arguments)
			if err != nil {
				return "", err
			}
			history = append(history, Turn{
				Role: "tool",
				ToolResult: &ToolResult{
					Name:   call.Name,
					Result: result,
				},
			})
		}
	}

	return "", fmt.Errorf("tool calling exceeded max steps")
}
