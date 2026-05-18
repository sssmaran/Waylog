package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAnthropicModel = "claude-sonnet-4-6"
const defaultAnthropicBaseURL = "https://api.anthropic.com/v1"
const anthropicVersion = "2023-06-01"

type AnthropicClient struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		APIKey:  apiKey,
		Model:   defaultAnthropicModel,
		BaseURL: defaultAnthropicBaseURL,
	}
}

func (c *AnthropicClient) Generate(ctx context.Context, prompt string, tools []ToolDefinition, history []Turn) (Result, error) {
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("anthropic api key required")
	}
	reqBody, err := c.buildRequest(prompt, tools, history)
	if err != nil {
		return Result{}, err
	}

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultAnthropicBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/messages", bytes.NewReader(reqBody))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, &ProviderError{Provider: "anthropic", Retryable: true, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, &ProviderError{Provider: "anthropic", Retryable: true, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &ProviderError{
			Provider:   "anthropic",
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode == 429 || resp.StatusCode >= 500,
			Message:    string(body),
		}
	}
	return parseAnthropicResponse(body)
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

func (c *AnthropicClient) buildRequest(prompt string, tools []ToolDefinition, history []Turn) ([]byte, error) {
	model := c.Model
	if model == "" {
		model = defaultAnthropicModel
	}
	req := anthropicRequest{
		Model:     model,
		MaxTokens: 1024,
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	}
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	nextToolID := 1
	lastToolID := ""
	for _, turn := range history {
		switch {
		case turn.ToolCall != nil:
			toolID := fmt.Sprintf("toolu_waylog_%d", nextToolID)
			nextToolID++
			lastToolID = toolID
			input := turn.ToolCall.Arguments
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			req.Messages = append(req.Messages, anthropicMessage{
				Role: "assistant",
				Content: []anthropicContentBlock{{
					Type:  "tool_use",
					ID:    toolID,
					Name:  turn.ToolCall.Name,
					Input: input,
				}},
			})
		case turn.ToolResult != nil:
			toolID := lastToolID
			if toolID == "" {
				toolID = "toolu_waylog_0"
			}
			payload, err := json.Marshal(turn.ToolResult.Result)
			if err != nil {
				return nil, fmt.Errorf("anthropic: marshal tool result: %w", err)
			}
			req.Messages = append(req.Messages, anthropicMessage{
				Role: "user",
				Content: []anthropicContentBlock{{
					Type:      "tool_result",
					ToolUseID: toolID,
					Content:   string(payload),
				}},
			})
		case turn.Text != "":
			req.Messages = append(req.Messages, anthropicMessage{Role: "assistant", Content: turn.Text})
		}
	}

	return json.Marshal(req)
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
}

func parseAnthropicResponse(body []byte) (Result, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{}, err
	}
	var out Result
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			out.Text += block.Text
		case "tool_use":
			args := block.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{Name: block.Name, Arguments: args})
		}
	}
	return out, nil
}
