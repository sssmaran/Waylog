package llm

import (
	"context"
	"encoding/json"
)

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
}

type ToolResult struct {
	Name   string
	Result any
}

type Turn struct {
	Role       string
	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

type Result struct {
	Text      string
	ToolCalls []ToolCall
}

type Provider interface {
	Generate(ctx context.Context, prompt string, tools []ToolDefinition, history []Turn) (Result, error)
}
