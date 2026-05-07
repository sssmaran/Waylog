package llm

import (
	"strings"
	"testing"
)

func clearProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WAYLOG_LLM_PROVIDER", "")
	t.Setenv("WAYLOG_LLM_MODEL", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_MODEL", "")
	t.Setenv("GEMINI_API_BASE", "")
	t.Setenv("GEMINI_TOOL_MODE", "")
}

func TestSelectFromEnv_NoEnv(t *testing.T) {
	clearProviderEnv(t)

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Provider != "none" {
		t.Errorf("Provider = %q, want %q", sel.Provider, "none")
	}
	if sel.Configured {
		t.Error("Configured = true, want false")
	}
	if sel.AskEnabled {
		t.Error("AskEnabled = true, want false")
	}
	if sel.Impl != nil {
		t.Error("Impl != nil, want nil")
	}
}

func TestSelectFromEnv_NoneExplicit(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("WAYLOG_LLM_PROVIDER", "none")
	t.Setenv("GEMINI_API_KEY", "ignored")

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Provider != "none" {
		t.Errorf("Provider = %q, want %q", sel.Provider, "none")
	}
	if !sel.Configured {
		t.Error("Configured = false, want true")
	}
	if sel.AskEnabled {
		t.Error("AskEnabled = true, want false")
	}
	if sel.Model != "" || sel.ToolMode != "" {
		t.Errorf("model/tool mode should be empty for none, got %q/%q", sel.Model, sel.ToolMode)
	}
	if sel.Impl != nil {
		t.Error("Impl != nil, want nil")
	}
}

func TestSelectFromEnv_GeminiWithKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("WAYLOG_LLM_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "test-key")

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", sel.Provider, "gemini")
	}
	if !sel.Configured {
		t.Error("Configured = false, want true")
	}
	if !sel.AskEnabled {
		t.Error("AskEnabled = false, want true")
	}
	if sel.Impl == nil {
		t.Error("Impl = nil, want non-nil")
	}
	if sel.Model == "" {
		t.Error("Model is empty, want default")
	}
}

func TestSelectFromEnv_GeminiInferredFromKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("GOOGLE_API_KEY", "test-key")

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", sel.Provider, "gemini")
	}
	if !sel.AskEnabled {
		t.Error("AskEnabled = false, want true")
	}
}

func TestSelectFromEnv_GeminiMissingKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("WAYLOG_LLM_PROVIDER", "gemini")

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", sel.Provider, "gemini")
	}
	if sel.Configured {
		t.Error("Configured = true, want false")
	}
	if sel.AskEnabled {
		t.Error("AskEnabled = true, want false")
	}
	if sel.Impl != nil {
		t.Error("Impl != nil, want nil")
	}
}

func TestSelectFromEnv_UnknownProvider(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("WAYLOG_LLM_PROVIDER", "anthropic")

	_, err := SelectFromEnv()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error %q should mention provider name", err.Error())
	}
	if !strings.Contains(err.Error(), "none, gemini") {
		t.Errorf("error %q should list supported providers", err.Error())
	}
}

func TestSelectFromEnv_WaylogModelOverridesGeminiModel(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("WAYLOG_LLM_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("WAYLOG_LLM_MODEL", "foo")
	t.Setenv("GEMINI_MODEL", "bar")

	sel, err := SelectFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Model != "foo" {
		t.Errorf("Model = %q, want %q", sel.Model, "foo")
	}
}
