package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func Run(args []string) {
	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "help":
		usage()
	case "waylog":
		handleAsk(args[1:])
	default:
		usage()
	}
}

func usage() {
	fmt.Println("usage:")
	fmt.Println("  waylog \"<question>\"")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  waylog \"show top errors\"")
	fmt.Println("  waylog \"trace summary for trace <trace-id>\"")
	fmt.Println("  waylog \"explain request <trace-id>\"")
	fmt.Println("  waylog \"graph_query expr='error_code=PMT_502' window='10m'\"")
	fmt.Println("  waylog \"compare_windows current='10m' baseline='10m' offset='1h'\"")
}

func handleAsk(args []string) {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Println("usage: waylog \"<question>\"")
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
		return reg.Call(ctx, ingest.GlobalGraphStore, name, params)
	}), prompt, 5)
	if err != nil {
		printAskError(err)
		return
	}

	fmt.Println(answer)
}

func printAskError(err error) {
	msg := err.Error()
	fmt.Println("ask error:", err)

	switch {
	case strings.Contains(msg, "expr required") || strings.Contains(msg, "window required"):
		fmt.Println("tip: graph_query requires both expr and window, for example:")
		fmt.Println("  waylog \"graph_query expr='error_code=PMT_502' window='10m'\"")
	case strings.Contains(msg, "query parse error"):
		fmt.Println("tip: check your query syntax. Example:")
		fmt.Println("  waylog \"graph_query expr='success=false' window='10m'\"")
	case strings.Contains(msg, "request_id or trace_id required"):
		fmt.Println("tip: provide a trace ID, for example:")
		fmt.Println("  waylog \"explain request <trace-id>\"")
	case strings.Contains(msg, "current, baseline, and offset required"):
		fmt.Println("tip: compare_windows needs current, baseline, and offset, for example:")
		fmt.Println("  waylog \"compare_windows current='10m' baseline='10m' offset='1h'\"")
	}
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
