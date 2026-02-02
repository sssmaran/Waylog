package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func Run(args []string) {
	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "graph":
		runGraph(args[1:])
	case "ask":
		handleAsk(args[1:])
	default:
		usage()
	}
}

var store *graphstore.Store

func SetStore(s *graphstore.Store) {
	store = s
}

func usage() {
	fmt.Println("usage:")
	fmt.Println(" graph failures [--tier=premium]")
	fmt.Println(" ask \"<question>\"")
}

func handleAsk(args []string) {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Println("usage: waylog ask \"<question>\"")
		return
	}

	loadDotEnv(".env")

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	}
	if apiKey == "" {
		fmt.Println("GEMINI_API_KEY (or GOOGLE_API_KEY) is required")
		return
	}

	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	baseURL := strings.TrimSpace(os.Getenv("GEMINI_API_BASE"))
	toolMode := strings.TrimSpace(os.Getenv("GEMINI_TOOL_MODE"))

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		fmt.Println("tool registry error:", err)
		return
	}

	toolDefs := make([]llm.ToolDefinition, 0, len(reg.List()))
	for _, t := range reg.List() {
		toolDefs = append(toolDefs, llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	client := llm.NewGeminiClient(apiKey)
	if model != "" {
		client.Model = model
	}
	if baseURL != "" {
		client.BaseURL = baseURL
	}
	if toolMode != "" {
		client.ToolMode = toolMode
	}

	answer, err := llm.Ask(context.Background(), client, toolDefs, llm.ToolExecutorFunc(func(ctx context.Context, name string, params json.RawMessage) (any, error) {
		return reg.Call(ctx, graphStore(), name, params)
	}), prompt, 5)
	if err != nil {
		fmt.Println("ask error:", err)
		return
	}

	fmt.Println(answer)
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		val = strings.Trim(val, `"'`)
		_ = os.Setenv(key, val)
	}
}
