package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// allTools returns the v1.0 surviving tool ledger.
func allTools() []ToolDefinition {
	names := []string{
		"explain_request",
		"blast_radius",
		"triage_incident",
		"render_triage_report",
	}
	tools := make([]ToolDefinition, len(names))
	for i, n := range names {
		tools[i] = ToolDefinition{Name: n, Description: n}
	}
	return tools
}

func toolNames(tools []ToolDefinition) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func TestFilterToolsForPrompt(t *testing.T) {
	tests := []struct {
		name     string
		prompt   string
		expected []string
	}{
		{
			name:     "trace keyword routes to explain_request",
			prompt:   "show me the trace for abc123",
			expected: []string{"explain_request"},
		},
		{
			name:     "explain keyword routes to explain_request",
			prompt:   "explain why checkout failed",
			expected: []string{"explain_request"},
		},
		{
			name:     "root cause routes to explain_request",
			prompt:   "what is the root cause of the checkout failure",
			expected: []string{"explain_request"},
		},
		{
			name:     "why did routes to explain_request",
			prompt:   "why did checkout return 502",
			expected: []string{"explain_request"},
		},
		{
			name:     "blast keyword routes to blast_radius",
			prompt:   "what is the blast radius of PMT_502",
			expected: []string{"blast_radius"},
		},
		{
			name:     "impact keyword routes to blast_radius",
			prompt:   "what is the impact of PMT_502",
			expected: []string{"blast_radius"},
		},
		{
			name:     "affected keyword routes to blast_radius",
			prompt:   "which users are affected",
			expected: []string{"blast_radius"},
		},
		{
			name:     "triage keyword routes to triage_incident",
			prompt:   "triage incident inc_42",
			expected: []string{"triage_incident"},
		},
		{
			name:     "incident keyword routes to triage_incident",
			prompt:   "show me the latest incident",
			expected: []string{"triage_incident"},
		},
		{
			name:     "report keyword routes to render_triage_report",
			prompt:   "render the triage report",
			expected: []string{"render_triage_report"},
		},
		{
			name:     "case insensitive Blast Radius",
			prompt:   "Blast Radius for PMT_502",
			expected: []string{"blast_radius"},
		},
		{
			name:     "empty prompt returns empty",
			prompt:   "",
			expected: nil,
		},
		{
			name:     "unrelated prompt returns empty",
			prompt:   "hello how are you",
			expected: nil,
		},
	}

	tools := allTools()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterToolsForPrompt(tools, tt.prompt)
			gotNames := toolNames(got)

			if len(tt.expected) == 0 && len(gotNames) == 0 {
				return // both nil/empty
			}
			if len(gotNames) != len(tt.expected) {
				t.Fatalf("prompt=%q\n  got  %v\n  want %v", tt.prompt, gotNames, tt.expected)
			}
			for i := range tt.expected {
				if gotNames[i] != tt.expected[i] {
					t.Fatalf("prompt=%q\n  got  %v\n  want %v", tt.prompt, gotNames, tt.expected)
				}
			}
		})
	}
}

func TestFillToolArgsFromPrompt(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		rawArgs    string
		prompt     string
		wantKey    string
		wantVal    string
		wantFilled bool
	}{
		{
			name:       "extract trace_id for explain_request",
			tool:       "explain_request",
			rawArgs:    `{}`,
			prompt:     "explain request abcdef1234567890abcdef1234567890",
			wantKey:    "trace_id",
			wantVal:    "abcdef1234567890abcdef1234567890",
			wantFilled: true,
		},
		{
			name:       "UUID trace_id extracted",
			tool:       "explain_request",
			rawArgs:    `{}`,
			prompt:     "trace 550e8400-e29b-41d4-a716-446655440000",
			wantKey:    "trace_id",
			wantVal:    "550e8400-e29b-41d4-a716-446655440000",
			wantFilled: true,
		},
		{
			name:       "does not overwrite existing arg",
			tool:       "explain_request",
			rawArgs:    `{"trace_id":"existing_id_abcdef1234567890"}`,
			prompt:     "show trace 0000000000000000aaaaaaaaaaaaaaaa",
			wantKey:    "trace_id",
			wantVal:    "existing_id_abcdef1234567890",
			wantFilled: true,
		},
		{
			name:       "no hex ID in prompt returns unchanged",
			tool:       "explain_request",
			rawArgs:    `{}`,
			prompt:     "show me the trace",
			wantFilled: false,
		},
		{
			name:       "unrelated tool returns unchanged",
			tool:       "blast_radius",
			rawArgs:    `{}`,
			prompt:     "blast radius for abcdef1234567890abcdef1234567890",
			wantFilled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, filled := fillToolArgsFromPrompt(tt.tool, json.RawMessage(tt.rawArgs), tt.prompt)
			if filled != tt.wantFilled {
				t.Fatalf("filled=%v, want %v", filled, tt.wantFilled)
			}
			if !tt.wantFilled {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(got, &m); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}
			val, ok := m[tt.wantKey]
			if !ok {
				t.Fatalf("missing key %q in %s", tt.wantKey, string(got))
			}
			if val != tt.wantVal {
				t.Fatalf("%s=%v, want %v", tt.wantKey, val, tt.wantVal)
			}
		})
	}
}

func TestExtractTraceID(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"hex after trace keyword", "trace abcdef1234567890abcdef1234567890", "abcdef1234567890abcdef1234567890"},
		{"UUID after trace keyword", "trace 550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"standalone UUID", "explain 550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"standalone hex", "explain abcdef1234567890abcdef1234567890", "abcdef1234567890abcdef1234567890"},
		{"no ID", "what happened", ""},
		{"short hex ignored", "trace abc123", ""},
		{"hex in quotes", `trace "abcdef1234567890abcdef1234567890"`, "abcdef1234567890abcdef1234567890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTraceID(tt.prompt)
			if got != tt.want {
				t.Fatalf("extractTraceID(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestIsHex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abcdef1234567890", true},                 // 16 chars, minimum
		{"ABCDEF1234567890", true},                 // uppercase
		{"abcdef1234567890abcdef1234567890", true}, // 32 chars
		{"abc123", false},                          // too short (<16)
		{"", false},                                // empty
		{"abcdef123456789g", false},                // invalid char 'g'
		{"abcdef12345678 0", false},                // space
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isHex(tt.input); got != tt.want {
				t.Fatalf("isHex(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"550E8400-E29B-41D4-A716-446655440000", true},   // uppercase
		{"550e8400e29b41d4a716446655440000", false},      // no dashes
		{"550e8400-e29b-41d4-a716-44665544000", false},   // 35 chars
		{"550e8400-e29b-41d4-a716-4466554400000", false}, // 37 chars
		{"", false},
		{"not-a-uuid-at-all-no-not-this-one!!", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isUUID(tt.input); got != tt.want {
				t.Fatalf("isUUID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestGeminiGenerate_NonOK_ReturnsProviderError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer ts.Close()

	c := &GeminiClient{APIKey: "test", BaseURL: ts.URL, Model: "test-model"}
	_, err := c.Generate(context.Background(), "hello", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if pe.Provider != "gemini" {
		t.Errorf("Provider = %q, want gemini", pe.Provider)
	}
	if pe.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", pe.StatusCode)
	}
	if !pe.Retryable {
		t.Error("expected Retryable=true for 429")
	}
}

func TestGeminiGenerate_TransportError_ReturnsProviderError(t *testing.T) {
	c := &GeminiClient{APIKey: "test", BaseURL: "http://localhost:1", Model: "test-model"}
	_, err := c.Generate(context.Background(), "hello", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if pe.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 for transport error", pe.StatusCode)
	}
	if !pe.Retryable {
		t.Error("expected Retryable=true for transport error")
	}
}

func TestTrimToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"abc123"`, "abc123"},
		{`'abc123'`, "abc123"},
		{"abc123.", "abc123"},
		{"(abc123)", "abc123"},
		{"[abc123]", "abc123"},
		{"`abc123`", "abc123"},
		{"abc123", "abc123"},
		{"  abc123  ", "abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := trimToken(tt.input); got != tt.want {
				t.Fatalf("trimToken(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
