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
// WAYLOG_LLM_PROVIDER may be "none", "gemini", "anthropic", or "openai".
// When unset, a supported provider key infers the provider; otherwise none.
// Model precedence: WAYLOG_LLM_MODEL > provider-specific model env > built-in default.
func SelectFromEnv() (Selection, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("WAYLOG_LLM_PROVIDER")))

	switch raw {
	case "":
		if key := geminiKeyFromEnv(); key != "" {
			return buildGemini(key, true), nil
		}
		if key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); key != "" {
			return buildAnthropic(key, true), nil
		}
		if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
			return buildOpenAI(key, true), nil
		}
		return Selection{Provider: "none"}, nil
	case "none":
		return Selection{Provider: "none", Configured: true}, nil
	case "gemini":
		key := geminiKeyFromEnv()
		if key == "" {
			return Selection{Provider: "gemini", Configured: false}, nil
		}
		return buildGemini(key, true), nil
	case "anthropic":
		key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
		if key == "" {
			return Selection{Provider: "anthropic", Configured: false}, nil
		}
		return buildAnthropic(key, true), nil
	case "openai":
		key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		if key == "" {
			return Selection{Provider: "openai", Configured: false}, nil
		}
		return buildOpenAI(key, true), nil
	default:
		return Selection{}, fmt.Errorf("unknown LLM provider %q; supported: none, gemini, anthropic, openai", raw)
	}
}

func geminiKeyFromEnv() string {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	return key
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

func buildAnthropic(key string, configured bool) Selection {
	client := NewAnthropicClient(key)
	if model := strings.TrimSpace(os.Getenv("WAYLOG_LLM_MODEL")); model != "" {
		client.Model = model
	} else if model := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); model != "" {
		client.Model = model
	}
	if base := strings.TrimSpace(os.Getenv("ANTHROPIC_API_BASE")); base != "" {
		client.BaseURL = base
	}
	return Selection{
		Provider:   "anthropic",
		Model:      client.Model,
		Configured: configured,
		AskEnabled: true,
		Impl:       client,
	}
}

func buildOpenAI(key string, configured bool) Selection {
	client := NewOpenAIClient(key)
	if model := strings.TrimSpace(os.Getenv("WAYLOG_LLM_MODEL")); model != "" {
		client.Model = model
	} else if model := strings.TrimSpace(os.Getenv("OPENAI_MODEL")); model != "" {
		client.Model = model
	}
	if base := strings.TrimSpace(os.Getenv("OPENAI_API_BASE")); base != "" {
		client.BaseURL = base
	}
	return Selection{
		Provider:   "openai",
		Model:      client.Model,
		Configured: configured,
		AskEnabled: true,
		Impl:       client,
	}
}
