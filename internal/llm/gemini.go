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

const defaultGeminiModel = "gemini-2.5-flash"
const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type GeminiClient struct {
	APIKey     string
	Model      string
	BaseURL    string
	ToolMode   string
	HTTPClient *http.Client
}

func NewGeminiClient(apiKey string) *GeminiClient {
	return &GeminiClient{
		APIKey:   apiKey,
		Model:    defaultGeminiModel,
		BaseURL:  defaultGeminiBaseURL,
		ToolMode: "text",
	}
}

func (c *GeminiClient) Generate(ctx context.Context, prompt string, tools []ToolDefinition, history []Turn) (Result, error) {
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("gemini api key required")
	}
	model := c.Model
	if model == "" {
		model = defaultGeminiModel
	}

	mode := strings.ToLower(strings.TrimSpace(c.ToolMode))
	if mode == "" {
		mode = "text"
	}

	reqBody, err := c.buildRequest(prompt, tools, history, mode)
	if err != nil {
		return Result{}, err
	}

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultGeminiBaseURL
	}
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, model, c.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("gemini error: %s", string(body))
	}

	if mode == "text" {
		return parseGeminiTextResponse(body, history, tools)
	}
	return parseGeminiResponse(body)
}

type geminiRequest struct {
	Contents         []geminiContent   `json:"contents"`
	Tools            []geminiToolGroup `json:"tools,omitempty"`
	GenerationConfig map[string]any    `json:"generationConfig,omitempty"`
}

type geminiToolGroup struct {
	FunctionDeclarations []geminiFunctionDecl `json:"function_declarations"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResult `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResult struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

func (c *GeminiClient) buildRequest(prompt string, tools []ToolDefinition, history []Turn, mode string) ([]byte, error) {
	if mode == "text" {
		return json.Marshal(geminiRequest{
			Contents: []geminiContent{
				{
					Role: "user",
					Parts: []geminiPart{
						{Text: buildTextPrompt(prompt, history, tools)},
					},
				},
			},
			GenerationConfig: map[string]any{
				"temperature": 0.2,
			},
		})
	}

	contents := []geminiContent{
		{
			Role: "user",
			Parts: []geminiPart{
				{Text: prompt},
			},
		},
	}

	for _, turn := range history {
		switch {
		case turn.ToolCall != nil:
			args, err := decodeArgs(turn.ToolCall.Arguments)
			if err != nil {
				return nil, err
			}
			contents = append(contents, geminiContent{
				Role: "model",
				Parts: []geminiPart{
					{
						FunctionCall: &geminiFunctionCall{
							Name: turn.ToolCall.Name,
							Args: args,
						},
					},
				},
			})
		case turn.ToolResult != nil:
			contents = append(contents, geminiContent{
				Role: "tool",
				Parts: []geminiPart{
					{
						FunctionResponse: &geminiFunctionResult{
							Name:     turn.ToolResult.Name,
							Response: turn.ToolResult.Result,
						},
					},
				},
			})
		case turn.Text != "":
			contents = append(contents, geminiContent{
				Role: "model",
				Parts: []geminiPart{
					{Text: turn.Text},
				},
			})
		}
	}

	var toolGroups []geminiToolGroup
	if mode == "function" && len(tools) > 0 {
		group := geminiToolGroup{}
		for _, t := range tools {
			schema := sanitizeGeminiSchema(t.InputSchema)
			group.FunctionDeclarations = append(group.FunctionDeclarations, geminiFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			})
		}
		toolGroups = append(toolGroups, group)
	}

	req := geminiRequest{
		Contents: contents,
		GenerationConfig: map[string]any{
			"temperature": 0.2,
		},
	}
	if len(toolGroups) > 0 {
		req.Tools = toolGroups
	}

	return json.Marshal(req)
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid tool args: %w", err)
	}
	return out, nil
}

func buildTextPrompt(prompt string, history []Turn, tools []ToolDefinition) string {
	if hasToolResult(history) {
		return buildAnswerPrompt(prompt, history)
	}
	return fmt.Sprintf(
		"User said: %s\nChoose exactly one tool from the list below and return ONLY JSON in the form {\"name\":\"tool_name\",\"arguments\":{...}}.\nIf none apply, return {\"name\":\"NO_TOOL\",\"arguments\":{}}.\n\nAvailable tools:\n%s",
		prompt,
		buildToolList(tools),
	)
}

func buildAnswerPrompt(prompt string, history []Turn) string {
	var b strings.Builder
	b.WriteString("User question: ")
	b.WriteString(prompt)
	b.WriteString("\nTool results:\n")
	for _, turn := range history {
		if turn.ToolResult == nil {
			continue
		}
		payload, _ := json.Marshal(turn.ToolResult.Result)
		b.WriteString("- ")
		b.WriteString(turn.ToolResult.Name)
		b.WriteString(": ")
		b.Write(payload)
		b.WriteString("\n")
	}
	b.WriteString("Provide a concise, direct answer to the user.")
	return b.String()
}

func hasToolResult(history []Turn) bool {
	for _, turn := range history {
		if turn.ToolResult != nil {
			return true
		}
	}
	return false
}

func sanitizeGeminiSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	clean := stripAdditionalProperties(v)
	out, err := json.Marshal(clean)
	if err != nil {
		return raw
	}
	return out
}

func stripAdditionalProperties(v any) any {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "additionalProperties")
		for k, v := range t {
			t[k] = stripAdditionalProperties(v)
		}
		return t
	case []any:
		for i, v := range t {
			t[i] = stripAdditionalProperties(v)
		}
		return t
	default:
		return v
	}
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

func parseGeminiResponse(body []byte) (Result, error) {
	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{}, err
	}
	if len(resp.Candidates) == 0 {
		return Result{}, fmt.Errorf("gemini: no candidates")
	}

	var out Result
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			out.Text += part.Text
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				Name:      part.FunctionCall.Name,
				Arguments: args,
			})
		}
	}
	return out, nil
}

func parseGeminiTextResponse(body []byte, history []Turn, tools []ToolDefinition) (Result, error) {
	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Result{}, err
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return Result{}, fmt.Errorf("gemini: no candidates")
	}

	text := resp.Candidates[0].Content.Parts[0].Text
	if hasToolResult(history) {
		return Result{Text: strings.TrimSpace(text)}, nil
	}

	name, args, err := parseToolCallFromText(text)
	if err != nil {
		return Result{}, err
	}
	if name == "NO_TOOL" {
		return Result{Text: "No suitable tool found for that request."}, nil
	}
	if !toolExists(name, tools) {
		return Result{}, fmt.Errorf("unsupported tool requested: %s", name)
	}
	return Result{
		ToolCalls: []ToolCall{
			{
				Name:      name,
				Arguments: args,
			},
		},
	}, nil
}

func parseToolCallFromText(text string) (string, json.RawMessage, error) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "", nil, fmt.Errorf("empty tool call response")
	}
	raw = stripCodeFences(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end <= start {
		return "", nil, fmt.Errorf("no json object found in tool call response")
	}
	raw = raw[start : end+1]

	var payload struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", nil, fmt.Errorf("invalid tool call json: %w", err)
	}
	if payload.Name == "" {
		return "", nil, fmt.Errorf("tool call missing name")
	}
	if len(payload.Arguments) == 0 {
		payload.Arguments = json.RawMessage("{}")
	}
	return payload.Name, payload.Arguments, nil
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSpace(s)
		if i := strings.Index(s, "\n"); i != -1 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j != -1 {
			s = s[:j]
		}
	}
	return strings.TrimSpace(s)
}

func buildToolList(tools []ToolDefinition) string {
	if len(tools) == 0 {
		return "- (no tools available)"
	}
	var b strings.Builder
	for _, t := range tools {
		b.WriteString("- ")
		b.WriteString(t.Name)
		if t.Description != "" {
			b.WriteString(": ")
			b.WriteString(t.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func toolExists(name string, tools []ToolDefinition) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
