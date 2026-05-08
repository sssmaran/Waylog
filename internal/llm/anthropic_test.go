package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicGenerateSendsMessagesAndTools(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("path = %q, want /messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"done"}]}`))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key")
	client.BaseURL = srv.URL
	client.Model = "claude-test"
	res, err := client.Generate(context.Background(), "hello", []ToolDefinition{{
		Name:        "triage_incident",
		Description: "triage",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"incident_id":{"type":"string"}}}`),
	}}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("Text = %q, want done", res.Text)
	}
	if captured.Model != "claude-test" {
		t.Fatalf("model = %q", captured.Model)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", captured.Messages)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "triage_incident" {
		t.Fatalf("tools = %+v", captured.Tools)
	}
}

func TestParseAnthropicResponseToolUse(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"toolu_1","name":"triage_incident","input":{"incident_id":"inc_abc","snapshot":true}}
		]
	}`)
	res, err := parseAnthropicResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "checking" {
		t.Fatalf("Text = %q", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "triage_incident" {
		t.Fatalf("tool name = %q", res.ToolCalls[0].Name)
	}
	var args struct {
		IncidentID string `json:"incident_id"`
		Snapshot   bool   `json:"snapshot"`
	}
	if err := json.Unmarshal(res.ToolCalls[0].Arguments, &args); err != nil {
		t.Fatalf("args: %v", err)
	}
	if args.IncidentID != "inc_abc" || !args.Snapshot {
		t.Fatalf("args = %+v", args)
	}
}

func TestAnthropicGenerateAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key")
	client.BaseURL = srv.URL
	_, err := client.Generate(context.Background(), "hello", nil, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("err = %T, want *ProviderError", err)
	}
	if pe.Provider != "anthropic" || !pe.Retryable || pe.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("provider error = %+v", pe)
	}
}
