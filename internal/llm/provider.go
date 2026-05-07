package llm

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrProviderNotConfigured is returned when Ask cannot construct a provider
// from the current environment.
var ErrProviderNotConfigured = errors.New("LLM provider not configured; set WAYLOG_LLM_PROVIDER and provider credentials")

// Selection describes the resolved LLM provider state.
type Selection struct {
	Provider   string
	Model      string
	ToolMode   string
	Configured bool
	AskEnabled bool
	Impl       Provider
}

// SelectFromEnv resolves the LLM provider from environment variables.
//
// WAYLOG_LLM_PROVIDER may be "none" or "gemini". When unset, a Gemini key
// (GEMINI_API_KEY or GOOGLE_API_KEY) infers gemini; otherwise none.
// Model precedence: WAYLOG_LLM_MODEL > GEMINI_MODEL > built-in default.
func SelectFromEnv() (Selection, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("WAYLOG_LLM_PROVIDER")))
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}

	switch raw {
	case "":
		if key == "" {
			return Selection{Provider: "none"}, nil
		}
		return buildGemini(key, true), nil
	case "none":
		return Selection{Provider: "none", Configured: true}, nil
	case "gemini":
		if key == "" {
			return Selection{Provider: "gemini", Configured: false}, nil
		}
		return buildGemini(key, true), nil
	default:
		return Selection{}, fmt.Errorf("unknown LLM provider %q; supported: none, gemini", raw)
	}
}

func buildGemini(key string, configured bool) Selection {
	client := NewGeminiClient(key)
	if model := strings.TrimSpace(os.Getenv("WAYLOG_LLM_MODEL")); model != "" {
		client.Model = model
	} else if model := strings.TrimSpace(os.Getenv("GEMINI_MODEL")); model != "" {
		client.Model = model
	}
	if base := strings.TrimSpace(os.Getenv("GEMINI_API_BASE")); base != "" {
		client.BaseURL = base
	}
	if mode := strings.TrimSpace(os.Getenv("GEMINI_TOOL_MODE")); mode != "" {
		client.ToolMode = mode
	}
	return Selection{
		Provider:   "gemini",
		Model:      client.Model,
		ToolMode:   client.ToolMode,
		Configured: configured,
		AskEnabled: true,
		Impl:       client,
	}
}
