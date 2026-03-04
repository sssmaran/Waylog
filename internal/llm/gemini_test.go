package llm

import (
	"encoding/json"
	"testing"
)

// allTools returns the full tool set matching what RegisterGraphTools creates.
func allTools() []ToolDefinition {
	names := []string{
		"graph_stats",
		"explain_request",
		"trace_graph",
		"trace_summary",
		"graph_failures",
		"failure_patterns",
		"blast_radius",
		"failure_chain",
		"graph_query",
		"compare_windows",
		"graph_insights",
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
		// --- Keyword matches ---
		{
			name:     "trace keyword selects trace tools",
			prompt:   "show me the trace for abc123",
			expected: []string{"trace_summary", "trace_graph", "explain_request"},
		},
		{
			name:     "explain keyword selects explain_request",
			prompt:   "explain why checkout failed",
			expected: []string{"explain_request"},
		},
		{
			name:     "info keyword selects explain_request",
			prompt:   "info about request abc",
			expected: []string{"explain_request"},
		},
		{
			name:     "blast keyword selects blast_radius",
			prompt:   "what is the blast radius of PMT_502",
			expected: []string{"blast_radius"},
		},
		{
			name:     "pattern keyword selects failure_patterns",
			prompt:   "show me failure pattern in the last hour",
			expected: []string{"failure_patterns"},
		},
		{
			name:     "diff keyword selects compare_windows",
			prompt:   "diff errors between now and 1h ago",
			expected: []string{"compare_windows"},
		},
		{
			name:     "compare keyword selects compare_windows",
			prompt:   "compare errors in last 10m vs 1h ago",
			expected: []string{"compare_windows"},
		},
		{
			name:     "query keyword selects graph_query",
			prompt:   "query for error_code=PMT_502 in last 10m",
			expected: []string{"graph_query"},
		},
		{
			name:     "insight keyword selects graph_insights",
			prompt:   "show insights for the last hour",
			expected: []string{"graph_insights"},
		},
		{
			name:     "top keyword selects graph_insights",
			prompt:   "top errors in the last 10 minutes",
			expected: []string{"graph_insights"},
		},
		{
			name:     "stats keyword selects graph_insights",
			prompt:   "show me stats",
			expected: []string{"graph_insights"},
		},
		{
			name:     "failure keyword selects failures + insights",
			prompt:   "list all failures",
			expected: []string{"graph_failures", "graph_insights"},
		},
		{
			name:     "error keyword selects failures + insights",
			prompt:   "what errors happened recently",
			expected: []string{"graph_failures", "graph_insights"},
		},
		{
			name:     "service path selects trace_summary + failure_chain",
			prompt:   "show the service path for this request",
			expected: []string{"trace_summary", "failure_chain"},
		},
		{
			name:     "path keyword selects trace_summary + failure_chain",
			prompt:   "what is the path of the request",
			expected: []string{"trace_summary", "failure_chain"},
		},

		// --- Case insensitivity ---
		{
			name:     "case insensitive TRACE",
			prompt:   "Show TRACE for abc123",
			expected: []string{"trace_summary", "trace_graph", "explain_request"},
		},
		{
			name:     "case insensitive Blast Radius",
			prompt:   "Blast Radius for PMT_502",
			expected: []string{"blast_radius"},
		},

		// --- No match → empty (fallback to full list happens in caller) ---
		{
			name:     "why did checkout break routes to explain_request",
			prompt:   "why did checkout break",
			expected: []string{"explain_request", "failure_chain"},
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

		// --- Priority: first matching case wins (switch statement) ---
		{
			name:     "trace wins over error when both present",
			prompt:   "trace the error in payment service",
			expected: []string{"trace_summary", "trace_graph", "explain_request"},
		},
		{
			name:     "trace wins over explain when both present",
			prompt:   "explain this trace abc123",
			expected: []string{"trace_summary", "trace_graph", "explain_request"},
		},
		{
			name:     "path wins over error",
			prompt:   "show the path of the error",
			expected: []string{"trace_summary", "failure_chain"},
		},
		{
			name:     "explain wins over failure",
			prompt:   "explain the failure",
			expected: []string{"explain_request"},
		},

		// --- Previously known gaps, now fixed ---
		{
			name:     "impact keyword routes to blast_radius",
			prompt:   "what is the impact of PMT_502",
			expected: []string{"blast_radius"},
		},
		{
			name:     "affected keyword routes to blast_radius",
			prompt:   "which users are affected by the payment outage",
			expected: []string{"blast_radius"},
		},
		{
			name:     "what happened routes to graph_insights",
			prompt:   "what happened in the last 10 minutes",
			expected: []string{"graph_insights"},
		},
		{
			name:     "root cause routes to explain_request",
			prompt:   "what is the root cause of the checkout failure",
			expected: []string{"explain_request", "failure_chain"},
		},
		{
			name:     "overview routes to graph_insights",
			prompt:   "give me an overview of the system health",
			expected: []string{"graph_insights"},
		},

		// --- New synonym coverage ---
		{
			name:     "why did routes to explain_request",
			prompt:   "why did checkout return 502",
			expected: []string{"explain_request", "failure_chain"},
		},
		{
			name:     "why is routes to explain_request",
			prompt:   "why is the payment service failing",
			expected: []string{"explain_request", "failure_chain"},
		},
		{
			name:     "summary routes to graph_insights",
			prompt:   "give me a summary of errors",
			expected: []string{"graph_insights"},
		},
		{
			name:     "health routes to graph_insights",
			prompt:   "how is system health right now",
			expected: []string{"graph_insights"},
		},
		{
			name:     "radius keyword routes to blast_radius",
			prompt:   "show the error radius for DB_TIMEOUT",
			expected: []string{"blast_radius"},
		},

		// --- Remaining gaps (no keyword match) ---
		{
			name:     "GAP: vague question returns empty",
			prompt:   "is anything wrong with my services",
			expected: nil,
		},
		{
			name:     "GAP: latency question returns empty",
			prompt:   "which endpoints are slow right now",
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
			name:       "extract trace_id for trace_graph",
			tool:       "trace_graph",
			rawArgs:    `{}`,
			prompt:     "show trace abcdef1234567890abcdef1234567890",
			wantKey:    "trace_id",
			wantVal:    "abcdef1234567890abcdef1234567890",
			wantFilled: true,
		},
		{
			name:       "extract trace_id for trace_summary",
			tool:       "trace_summary",
			rawArgs:    `{}`,
			prompt:     "trace summary for abcdef1234567890abcdef1234567890",
			wantKey:    "trace_id",
			wantVal:    "abcdef1234567890abcdef1234567890",
			wantFilled: true,
		},
		{
			name:       "extract request_id for explain_request",
			tool:       "explain_request",
			rawArgs:    `{}`,
			prompt:     "explain request abcdef1234567890abcdef1234567890aabbccdd",
			wantKey:    "request_id",
			wantVal:    "abcdef1234567890abcdef1234567890aabbccdd",
			wantFilled: true,
		},
		{
			name:       "extract request_id for failure_chain",
			tool:       "failure_chain",
			rawArgs:    `{}`,
			prompt:     "failure chain for request abcdef1234567890abcdef1234567890aabbccdd",
			wantKey:    "request_id",
			wantVal:    "abcdef1234567890abcdef1234567890aabbccdd",
			wantFilled: true,
		},
		{
			name:       "does not overwrite existing arg",
			tool:       "trace_graph",
			rawArgs:    `{"trace_id":"existing_id_abcdef1234567890"}`,
			prompt:     "show trace 0000000000000000aaaaaaaaaaaaaaaa",
			wantKey:    "trace_id",
			wantVal:    "existing_id_abcdef1234567890",
			wantFilled: true,
		},
		{
			name:       "no hex ID in prompt returns unchanged",
			tool:       "trace_graph",
			rawArgs:    `{}`,
			prompt:     "show me the trace",
			wantFilled: false,
		},
		{
			name:       "unrelated tool returns unchanged",
			tool:       "graph_stats",
			rawArgs:    `{}`,
			prompt:     "show stats for abcdef1234567890abcdef1234567890",
			wantFilled: false,
		},
		{
			name:       "UUID trace_id extracted",
			tool:       "trace_graph",
			rawArgs:    `{}`,
			prompt:     "trace 550e8400-e29b-41d4-a716-446655440000",
			wantKey:    "trace_id",
			wantVal:    "550e8400-e29b-41d4-a716-446655440000",
			wantFilled: true,
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

func TestExtractRequestID(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"hex after request keyword", "request abcdef1234567890abcdef1234567890aabbccdd", "abcdef1234567890abcdef1234567890aabbccdd"},
		{"standalone hex", "explain abcdef1234567890abcdef1234567890aabbccdd", "abcdef1234567890abcdef1234567890aabbccdd"},
		{"no ID", "explain the failure", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRequestID(tt.prompt)
			if got != tt.want {
				t.Fatalf("extractRequestID(%q) = %q, want %q", tt.prompt, got, tt.want)
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
