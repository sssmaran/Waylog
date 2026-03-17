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

	toolsForPrompt := tools
	if mode == "text" {
		if filtered := filterToolsForPrompt(tools, prompt); len(filtered) > 0 {
			toolsForPrompt = filtered
		}
	}

	reqBody, err := c.buildRequest(prompt, toolsForPrompt, history, mode)
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
		return Result{}, &ProviderError{Provider: "gemini", Retryable: true, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, &ProviderError{Provider: "gemini", Retryable: true, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &ProviderError{
			Provider:   "gemini",
			StatusCode: resp.StatusCode,
			Retryable:  resp.StatusCode == 429 || resp.StatusCode >= 500,
			Message:    string(body),
		}
	}

	if mode == "text" {
		return parseGeminiTextResponse(body, prompt, history, toolsForPrompt)
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
	b.WriteString("\n\nTool results:\n")
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
	b.WriteString("\nProvide a concise, structured answer.\n")
	b.WriteString("Format:\n")
	b.WriteString("Title line summarizing the result.\n")
	b.WriteString("One short explanatory sentence after the title.\n")
	b.WriteString("Then sections with labels and bullet points using `- key: value`.\n")
	b.WriteString("Keep IDs on their own bullet lines for easy copy.\n")
	b.WriteString("If available, include the request type (flow/event) and service path.\n")
	b.WriteString("Do not include raw tool JSON in the response.\n")
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

func parseGeminiTextResponse(body []byte, prompt string, history []Turn, tools []ToolDefinition) (Result, error) {
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
		return Result{Text: "I couldn't map that to a tool. Try: \"show top errors\", \"summarize trace <trace-id>\", or \"explain request <request-id>\"."}, nil
	}
	if !toolExists(name, tools) {
		return Result{}, fmt.Errorf("unsupported tool requested: %s", name)
	}
	if filled, ok := fillToolArgsFromPrompt(name, args, prompt); ok {
		args = filled
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

func filterToolsForPrompt(tools []ToolDefinition, prompt string) []ToolDefinition {
	p := strings.ToLower(prompt)
	var out []ToolDefinition

	add := func(name string) {
		for _, t := range tools {
			if t.Name == name {
				out = append(out, t)
				return
			}
		}
	}

	switch {
	case strings.Contains(p, "trace"):
		add("trace_summary")
		add("trace_graph")
		add("explain_request")
	case strings.Contains(p, "service path") || strings.Contains(p, "path"):
		add("trace_summary")
		add("failure_chain")
	case strings.Contains(p, "root cause") || strings.Contains(p, "why did") || strings.Contains(p, "why is"):
		add("explain_request")
		add("failure_chain")
	case strings.Contains(p, "explain") || strings.Contains(p, "info"):
		add("explain_request")
	case strings.Contains(p, "impact") || strings.Contains(p, "affected") || strings.Contains(p, "blast") || strings.Contains(p, "radius"):
		add("blast_radius")
	case strings.Contains(p, "pattern"):
		add("failure_patterns")
	case strings.Contains(p, "diff") || strings.Contains(p, "compare"):
		add("compare_windows")
	case strings.Contains(p, "query"):
		add("graph_query")
	case strings.Contains(p, "insight") || strings.Contains(p, "top") || strings.Contains(p, "stats") ||
		strings.Contains(p, "overview") || strings.Contains(p, "summary") || strings.Contains(p, "health") ||
		strings.Contains(p, "what happened"):
		add("graph_insights")
	case strings.Contains(p, "failure") || strings.Contains(p, "error"):
		add("graph_failures")
		add("graph_insights")
	}

	return out
}

func fillToolArgsFromPrompt(tool string, raw json.RawMessage, prompt string) (json.RawMessage, bool) {
	args := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return raw, false
		}
	}

	setIfMissing := func(key, val string) {
		if val == "" {
			return
		}
		if _, ok := args[key]; ok {
			return
		}
		args[key] = val
	}

	switch tool {
	case "explain_request", "failure_chain":
		setIfMissing("request_id", extractRequestID(prompt))
	case "trace_graph":
		setIfMissing("trace_id", extractTraceID(prompt))
	case "trace_summary":
		setIfMissing("trace_id", extractTraceID(prompt))
		setIfMissing("request_id", extractRequestID(prompt))
	}

	if len(args) == 0 {
		return raw, false
	}
	out, err := json.Marshal(args)
	if err != nil {
		return raw, false
	}
	return out, true
}

func extractRequestID(prompt string) string {
	if id := extractHexIDAfterKeyword(prompt, "request"); id != "" {
		return id
	}
	return extractFirstHexID(prompt)
}

func extractTraceID(prompt string) string {
	if id := extractUUIDAfterKeyword(prompt, "trace"); id != "" {
		return id
	}
	if id := extractHexIDAfterKeyword(prompt, "trace"); id != "" {
		return id
	}
	if id := extractFirstUUID(prompt); id != "" {
		return id
	}
	return extractFirstHexID(prompt)
}

func extractHexIDAfterKeyword(prompt, keyword string) string {
	p := strings.ToLower(prompt)
	idx := strings.Index(p, keyword)
	if idx == -1 {
		return ""
	}
	rest := prompt[idx+len(keyword):]
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	candidate := trimToken(fields[0])
	if isHex(candidate) {
		return candidate
	}
	return ""
}

func extractUUIDAfterKeyword(prompt, keyword string) string {
	p := strings.ToLower(prompt)
	idx := strings.Index(p, keyword)
	if idx == -1 {
		return ""
	}
	rest := prompt[idx+len(keyword):]
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	candidate := trimToken(fields[0])
	if isUUID(candidate) {
		return candidate
	}
	return ""
}

func extractFirstHexID(prompt string) string {
	for _, token := range strings.Fields(prompt) {
		candidate := trimToken(token)
		if isHex(candidate) {
			return candidate
		}
	}
	return ""
}

func extractFirstUUID(prompt string) string {
	for _, token := range strings.Fields(prompt) {
		candidate := trimToken(token)
		if isUUID(candidate) {
			return candidate
		}
	}
	return ""
}

func trimToken(token string) string {
	return strings.Trim(token, " \t\n\r\"'`.,;:()[]{}<>")
}

func isHex(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
