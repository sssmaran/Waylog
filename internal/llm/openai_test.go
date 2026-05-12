package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIGenerateSendsResponsesRequest(t *testing.T) {
	var captured struct {
		Model string `json:"model"`
		Input []struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"input"`
		Tools []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"done","output":[]}`))
	}))
	defer srv.Close()

	client := NewOpenAIClient("test-key")
	client.BaseURL = srv.URL
	client.Model = "gpt-test"
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
	if captured.Model != "gpt-test" {
		t.Fatalf("model = %q", captured.Model)
	}
	if len(captured.Input) != 1 || captured.Input[0].Role != "user" || captured.Input[0].Content != "hello" {
		t.Fatalf("input = %+v", captured.Input)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Type != "function" || captured.Tools[0].Name != "triage_incident" {
		t.Fatalf("tools = %+v", captured.Tools)
	}
}

func TestParseOpenAIResponseFunctionCall(t *testing.T) {
	body := []byte(`{
		"output": [
			{"type":"function_call","call_id":"call_1","name":"triage_incident","arguments":"{\"incident_id\":\"inc_abc\",\"snapshot\":true}"}
		]
	}`)
	res, err := parseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "triage_incident" {
		t.Fatalf("tool name = %q", res.ToolCalls[0].Name)
	}
	if res.ToolCalls[0].ProviderID != "call_1" {
		t.Fatalf("ProviderID = %q, want call_1", res.ToolCalls[0].ProviderID)
	}
	if len(res.ToolCalls[0].ProviderRawItems) != 1 {
		t.Fatalf("ProviderRawItems len = %d, want 1", len(res.ToolCalls[0].ProviderRawItems))
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

func TestOpenAIRequestPreservesResponseOutputAndCallIDs(t *testing.T) {
	body := []byte(`{
		"output": [
			{"type":"reasoning","id":"rs_1","summary":[]},
			{"type":"function_call","call_id":"call_1","name":"triage_incident","arguments":"{\"incident_id\":\"inc_abc\"}"},
			{"type":"function_call","call_id":"call_2","name":"blast_radius","arguments":"{\"code\":\"PMT_502\"}"}
		]
	}`)
	res, err := parseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2", len(res.ToolCalls))
	}

	client := NewOpenAIClient("test-key")
	raw, err := client.buildRequest("triage", nil, []Turn{
		{ToolCall: &res.ToolCalls[0]},
		{ToolResult: &ToolResult{Name: "triage_incident", Result: map[string]string{"report_hash": "sha256:x"}}},
		{ToolCall: &res.ToolCalls[1]},
		{ToolResult: &ToolResult{Name: "blast_radius", Result: map[string]int{"requests": 12}}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}

	var req struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	var types []string
	var callIDs []string
	for _, item := range req.Input {
		var got struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(item, &got); err != nil {
			t.Fatalf("decode input item: %v", err)
		}
		if got.Type == "" {
			continue
		}
		types = append(types, got.Type)
		if got.CallID != "" {
			callIDs = append(callIDs, got.CallID)
		}
	}
	wantTypes := []string{"reasoning", "function_call", "function_call", "function_call_output", "function_call_output"}
	if len(types) != len(wantTypes) {
		t.Fatalf("types = %v, want %v", types, wantTypes)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Fatalf("types = %v, want %v", types, wantTypes)
		}
	}
	wantCallIDs := []string{"call_1", "call_2", "call_1", "call_2"}
	if len(callIDs) != len(wantCallIDs) {
		t.Fatalf("callIDs = %v, want %v", callIDs, wantCallIDs)
	}
	for i := range wantCallIDs {
		if callIDs[i] != wantCallIDs[i] {
			t.Fatalf("callIDs = %v, want %v", callIDs, wantCallIDs)
		}
	}
}

func TestParseOpenAIResponseMessageText(t *testing.T) {
	body := []byte(`{
		"output": [
			{"type":"message","content":[{"type":"output_text","text":"hello"}]}
		]
	}`)
	res, err := parseOpenAIResponse(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Text != "hello" {
		t.Fatalf("Text = %q, want hello", res.Text)
	}
}

func TestOpenAIDefaultModel(t *testing.T) {
	client := NewOpenAIClient("test-key")
	if client.Model != "gpt-4o-mini" {
		t.Fatalf("default model = %q, want gpt-4o-mini", client.Model)
	}
	if defaultOpenAIModel != "gpt-4o-mini" {
		t.Fatalf("defaultOpenAIModel = %q, want gpt-4o-mini", defaultOpenAIModel)
	}
}

func TestOpenAIGenerateAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewOpenAIClient("test-key")
	client.BaseURL = srv.URL
	_, err := client.Generate(context.Background(), "hello", nil, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("err = %T, want *ProviderError", err)
	}
	if pe.Provider != "openai" || !pe.Retryable || pe.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("provider error = %+v", pe)
	}
}
