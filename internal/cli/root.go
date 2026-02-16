package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/llm"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// defaultStore is set via SetDefaultStore for backward compatibility.
var defaultStore tools.Store

// SetDefaultStore sets the default store for CLI commands that don't provide one.
func SetDefaultStore(s tools.Store) {
	defaultStore = s
}

// Run runs the CLI with the default store.
func Run(args []string) {
	RunWithStore(defaultStore, args)
}

// RunWithStore runs the CLI with the provided store.
func RunWithStore(store tools.Store, args []string) {
	if len(args) == 0 {
		usage()
		return
	}

	switch args[0] {
	case "help":
		usage()
	case "waylog":
		handleAsk(store, args[1:])
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

func handleAsk(store tools.Store, args []string) {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt == "" {
		fmt.Println("usage: waylog \"<question>\"")
		return
	}

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
		return reg.Call(ctx, store, name, params)
	}), prompt, 5)
	if err != nil {
		printAskError(err)
		return
	}

	fmt.Println(colorizeOutput(answer))
}

func printAskError(err error) {
	msg := err.Error()
	fmt.Printf("%s✗ %s%s\n", ansiRed, msg, ansiReset)

	var tip string
	switch {
	case strings.Contains(msg, "expr required") || strings.Contains(msg, "window required"):
		tip = "graph_query requires both expr and window, for example:\n  waylog \"graph_query expr='error_code=PMT_502' window='10m'\""
	case strings.Contains(msg, "query parse error"):
		tip = "check your query syntax. Example:\n  waylog \"graph_query expr='success=false' window='10m'\""
	case strings.Contains(msg, "request_id or trace_id required"):
		tip = "provide a trace ID, for example:\n  waylog \"explain request <trace-id>\""
	case strings.Contains(msg, "current, baseline, and offset required"):
		tip = "compare_windows needs current, baseline, and offset, for example:\n  waylog \"compare_windows current='10m' baseline='10m' offset='1h'\""
	case strings.Contains(msg, "map that to a tool") || strings.Contains(msg, "couldn't"):
		tip = "Try: \"show top errors\", \"summarize trace <id>\", or \"explain request <id>\""
	}
	if tip != "" {
		fmt.Printf("%s💡 %s%s\n", ansiYellow, tip, ansiReset)
	}
}

const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiRed     = "\033[31m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiMagenta = "\033[35m"
	ansiCyan    = "\033[36m"
	ansiWhite   = "\033[37m"
)

func colorizeOutput(s string) string {
	lines := strings.Split(s, "\n")
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(colorizeLine(line))
	}
	return out.String()
}

func colorizeLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// Empty lines
	if trimmed == "" {
		return ""
	}

	// Section headers: **Title** or **Title**
	if strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**") {
		title := strings.Trim(trimmed, "* ")
		return fmt.Sprintf("\n%s%s%s%s", ansiBold, ansiCyan, title, ansiReset)
	}

	// Bullet lines: - key: value
	if strings.HasPrefix(trimmed, "- ") {
		return colorizeBullet(trimmed)
	}

	// Title line (first non-empty, non-bullet, non-header line) — bold white
	if !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "*") {
		return fmt.Sprintf("%s%s%s", ansiWhite, trimmed, ansiReset)
	}

	return line
}

func colorizeBullet(line string) string {
	// Split "- key: value" into key and value
	content := strings.TrimPrefix(line, "- ")
	parts := strings.SplitN(content, ": ", 2)

	if len(parts) != 2 {
		return fmt.Sprintf("  %s•%s %s", ansiDim, ansiReset, content)
	}

	key := parts[0]
	value := parts[1]

	// Color the value based on the key type
	coloredValue := colorizeValue(key, value)
	return fmt.Sprintf("  %s•%s %s%s:%s %s", ansiDim, ansiReset, ansiDim, key, ansiReset, coloredValue)
}

func colorizeValue(key, value string) string {
	lowerKey := strings.ToLower(key)
	lowerVal := strings.ToLower(value)

	// Error codes and error-related values
	if strings.Contains(lowerKey, "error") || strings.Contains(lowerKey, "failure") {
		return fmt.Sprintf("%s%s%s%s", ansiBold, ansiRed, value, ansiReset)
	}

	// Trace/request/span IDs — cyan for easy copy
	if strings.Contains(lowerKey, "trace_id") || strings.Contains(lowerKey, "request_id") ||
		strings.Contains(lowerKey, "span") {
		return fmt.Sprintf("%s%s%s", ansiCyan, value, ansiReset)
	}

	// Counts and numeric values
	if strings.Contains(lowerKey, "count") || strings.Contains(lowerKey, "total") ||
		strings.Contains(lowerKey, "latency") {
		return fmt.Sprintf("%s%s%s%s", ansiBold, ansiYellow, value, ansiReset)
	}

	// Service names
	if strings.Contains(lowerKey, "service") {
		return fmt.Sprintf("%s%s%s", ansiMagenta, value, ansiReset)
	}

	// Event names
	if strings.Contains(lowerKey, "event") || strings.Contains(lowerKey, "flow") {
		return fmt.Sprintf("%s%s%s", ansiBlue, value, ansiReset)
	}

	// Service paths (arrows)
	if strings.Contains(value, "->") {
		return fmt.Sprintf("%s%s%s", ansiMagenta, value, ansiReset)
	}

	// Success/failure status values
	if lowerVal == "true" || lowerVal == "success" || lowerVal == "ok" {
		return fmt.Sprintf("%s%s%s", ansiGreen, value, ansiReset)
	}
	if lowerVal == "false" || strings.Contains(lowerVal, "fail") || strings.Contains(lowerVal, "error") {
		return fmt.Sprintf("%s%s%s", ansiRed, value, ansiReset)
	}

	// Default — white
	return fmt.Sprintf("%s%s%s", ansiWhite, value, ansiReset)
}
