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

const defaultOpenAIModel = "gpt-5.4-mini"
const defaultOpenAIBaseURL = "https://api.openai.com/v1"

type OpenAIClient struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func NewOpenAIClient(apiKey string) *OpenAIClient {
	return &OpenAIClient{
		APIKey:  apiKey,
		Model:   defaultOpenAIModel,
		BaseURL: defaultOpenAIBaseURL,
	}
}

func (c *OpenAIClient) Generate(ctx context.Context, prompt string, tools []ToolDefinition, history []Turn) (Result, error) {
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("openai api key required")
	}
	reqBody, err := c.buildRequest(prompt, tools, history)
	if err != nil {
		return Result{}, err
	}

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/responses", bytes.NewReader(reqBody))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, &ProviderError{Provider: "openai", Retryable: true, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, &ProviderError{Provider: "openai", Retryable: true, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &ProviderError{
			Provider:   "openai",
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode == 429 || resp.StatusCode >= 500,
			Message:    string(body),
		}
	}
	return parseOpenAIResponse(body)
}

type openAIRequest struct {
	Model string            `json:"model"`
	Input []json.RawMessage `json:"input"`
	Tools []openAITool      `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIFunctionCallInput struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIFunctionOutputInput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type openAITool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (c *OpenAIClient) buildRequest(prompt string, tools []ToolDefinition, history []Turn) ([]byte, error) {
	model := c.Model
	if model == "" {
		model = defaultOpenAIModel
	}
	req := openAIRequest{
		Model: model,
		Input: []json.RawMessage{mustMarshalOpenAI(openAIMessage{Role: "user", Content: prompt})},
	}
	for _, t := range tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		req.Tools = append(req.Tools, openAITool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
		})
	}

	nextCallID := 1
	lastCallID := ""
	for _, turn := range history {
		switch {
		case turn.ToolCall != nil:
			callID := fmt.Sprintf("call_waylog_%d", nextCallID)
			nextCallID++
			if turn.ToolCall.ProviderID != "" {
				callID = turn.ToolCall.ProviderID
			}
			lastCallID = callID
			if len(turn.ToolCall.ProviderRawItems) > 0 {
				req.Input = append(req.Input, turn.ToolCall.ProviderRawItems...)
				continue
			}
			if !turn.ToolCall.ProviderRawIncluded {
				args := string(turn.ToolCall.Arguments)
				if args == "" {
					args = "{}"
				}
				req.Input = append(req.Input, mustMarshalOpenAI(openAIFunctionCallInput{
					Type:      "function_call",
					CallID:    callID,
					Name:      turn.ToolCall.Name,
					Arguments: args,
				}))
			}
		case turn.ToolResult != nil:
			callID := lastCallID
			if callID == "" {
				callID = "call_waylog_0"
			}
			payload, err := json.Marshal(turn.ToolResult.Result)
			if err != nil {
				return nil, fmt.Errorf("openai: marshal tool result: %w", err)
			}
			req.Input = append(req.Input, mustMarshalOpenAI(openAIFunctionOutputInput{
				Type:   "function_call_output",
				CallID: callID,
				Output: string(payload),
			}))
		case turn.Text != "":
			req.Input = append(req.Input, mustMarshalOpenAI(openAIMessage{Role: "assistant", Content: turn.Text}))
		}
	}

	return json.Marshal(req)
}

func mustMarshalOpenAI(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

type openAIResponse struct {
	OutputText string            `json:"output_text"`
	Output     []json.RawMessage `json:"output"`
}

type openAIOutputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
}

func parseOpenAIResponse(body []byte) (Result, error) {
	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{}, err
	}
	out := Result{Text: resp.OutputText}
	rawItems := append([]json.RawMessage(nil), resp.Output...)
	rawAttached := false
	for _, raw := range resp.Output {
		var item openAIOutputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return Result{}, err
		}
		switch item.Type {
		case "function_call":
			args := json.RawMessage(item.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			call := ToolCall{Name: item.Name, Arguments: args, ProviderID: item.CallID}
			if !rawAttached {
				call.ProviderRawItems = rawItems
				rawAttached = true
			} else {
				call.ProviderRawIncluded = true
			}
			out.ToolCalls = append(out.ToolCalls, call)
		case "message":
			if out.Text != "" {
				continue
			}
			for _, part := range item.Content {
				if part.Type == "output_text" || part.Type == "text" {
					out.Text += part.Text
				}
			}
		}
	}
	return out, nil
}
