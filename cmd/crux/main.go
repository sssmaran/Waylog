package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	cliv2 "github.com/sssmaran/WaylogCLI/internal/cli/v2"
)

func main() {
	globalArgs, rest, ingestURL := splitCruxArgs(os.Args[1:])
	if len(rest) > 0 {
		if rest[0] == "first-run" {
			os.Exit(runFirstRun(rest[1:]))
		}
		if rest[0] == "open" {
			disp := NewDispatcher(ingestURL, globalArgs, os.Stdin)
			res := disp.DispatchTokens(rest, os.Stdout, os.Stderr)
			if res.Kind == ResultError || res.Kind == ResultUsage || res.Kind == ResultUnknown {
				if res.ExitCode != 0 {
					os.Exit(res.ExitCode)
				}
				os.Exit(1)
			}
			return
		}
		os.Exit(cliv2.RunCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(runREPL(os.Stdin, os.Stdout, os.Stderr, ingestURL, globalArgs))
}

func runREPL(stdin io.Reader, stdout, stderr io.Writer, ingestURL string, globalArgs []string) int {
	disp := NewDispatcher(ingestURL, globalArgs, stdin)
	reader := bufio.NewReader(stdin)
	fmt.Fprintln(stdout, "Crux")
	fmt.Fprintf(stdout, "Connected: %s\n", disp.ingestURL)
	fmt.Fprintln(stdout, "Type help for commands. Type exit to leave.")
	fmt.Fprintln(stdout)
	for {
		fmt.Fprint(stdout, "crux> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(stdout)
			return 0
		}
		res := disp.Dispatch(line, stdout, stderr)
		switch res.Kind {
		case ResultExit:
			fmt.Fprintln(stdout, "bye")
			return 0
		case ResultError:
			if res.ExitCode != 0 {
				fmt.Fprintf(stderr, "(exit %d)\n", res.ExitCode)
			}
		}
	}
}

func splitCruxArgs(args []string) (globalArgs []string, rest []string, ingestURL string) {
	ingestURL = ""
	readKey := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--addr" || arg == "--api-key" || arg == "--timeout":
			if i+1 >= len(args) {
				rest = append(rest, arg)
				continue
			}
			value := args[i+1]
			switch arg {
			case "--addr":
				ingestURL = value
				globalArgs = append(globalArgs, "--addr", normalizeIngestURL(value))
			case "--api-key":
				readKey = value
				globalArgs = append(globalArgs, "--api-key", value)
			case "--timeout":
				globalArgs = append(globalArgs, "--timeout", value)
			}
			i++
		case strings.HasPrefix(arg, "--addr="):
			value := strings.TrimPrefix(arg, "--addr=")
			ingestURL = value
			globalArgs = append(globalArgs, "--addr", normalizeIngestURL(value))
		case strings.HasPrefix(arg, "--api-key="):
			value := strings.TrimPrefix(arg, "--api-key=")
			readKey = value
			globalArgs = append(globalArgs, "--api-key", value)
		case strings.HasPrefix(arg, "--timeout="):
			globalArgs = append(globalArgs, "--timeout", strings.TrimPrefix(arg, "--timeout="))
		default:
			rest = append(rest, arg)
		}
	}
	if ingestURL == "" {
		ingestURL = resolveIngestURL(nil)
		globalArgs = append([]string{"--addr", normalizeIngestURL(ingestURL)}, globalArgs...)
	}
	if readKey == "" {
		if key := os.Getenv("WAYLOG_READ_KEY"); key != "" {
			globalArgs = append(globalArgs, "--api-key", key)
		}
	}
	return globalArgs, rest, ingestURL
}

func resolveIngestURL(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--addr" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--addr="):
			return strings.TrimPrefix(args[i], "--addr=")
		}
	}
	if v := os.Getenv("INGEST_ADDR"); v != "" {
		return v
	}
	if v := os.Getenv("INGEST_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func normalizeIngestURL(raw string) string {
	return cliv2.NormalizeBaseURL(raw)
}
