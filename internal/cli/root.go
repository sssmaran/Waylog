package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
	case "tools":
		handleTools()
	case "waylog":
		if len(args) > 1 && args[1] == "tools" {
			handleTools()
			return
		}
		handleAsk(store, args[1:])
	default:
		usage()
	}
}

func usage() {
	fmt.Println("usage:")
	fmt.Println("  waylog \"<question>\"")
	fmt.Println("  waylog tools")
	fmt.Println("")
	fmt.Println("examples:")
	fmt.Println("  waylog \"explain request <trace-id>\"")
	fmt.Println("  waylog \"blast radius for payment-service/charge/PMT_502 in 15m\"")
	fmt.Println("  waylog \"triage incident <incident-id>\"")
}

func handleTools() {
	// Try fetching from the ingest server first.
	addr := strings.TrimSpace(os.Getenv("INGEST_ADDR"))
	if addr == "" {
		addr = "http://localhost:8080"
	}
	if !strings.HasPrefix(addr, "http") {
		addr = "http://" + addr
	}

	type toolEntry struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Examples    []string `json:"examples,omitempty"`
	}

	var entries []toolEntry

	resp, err := http.Get(addr + "/v1/tools")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				var result struct {
					Tools []toolEntry `json:"tools"`
				}
				if json.Unmarshal(body, &result) == nil && len(result.Tools) > 0 {
					entries = result.Tools
				}
			}
		}
	}

	if len(entries) == 0 {
		fmt.Println("no tools available (server returned empty list or unreachable)")
		return
	}

	// Print formatted table.
	fmt.Printf("\n%s%sAvailable Tools%s (%d)\n\n", ansiBold, ansiCyan, ansiReset, len(entries))
	for _, t := range entries {
		fmt.Printf("  %s%s%-20s%s %s\n", ansiBold, ansiYellow, t.Name, ansiReset, t.Description)
		for _, ex := range t.Examples {
			fmt.Printf("  %s%-20s%s %swaylog \"%s\"%s\n", ansiDim, "", ansiReset, ansiDim, ex, ansiReset)
		}
		fmt.Println()
	}
}

func handleAsk(_ tools.Store, args []string) {
	if len(args) > 0 && strings.TrimSpace(strings.Join(args, " ")) == "" {
		fmt.Println("usage: waylog \"<question>\"")
		return
	}
	fmt.Println("local ask is no longer wired; use the ingest server's /v1/ask endpoint")
	fmt.Println("  curl -H \"Authorization: Bearer $WAYLOG_AGENT_KEY\" \\")
	fmt.Println("       -d '{\"prompt\":\"...\"}' http://localhost:8080/v1/ask")
}

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)
