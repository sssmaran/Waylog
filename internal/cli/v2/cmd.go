package cliv2

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const version = "v2-phase-2"

type cliConfig struct {
	addr    string
	apiKey  string
	timeout time.Duration
	json    bool
}

func RunCLI(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	cfg, rest, control, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		printUsage(stderr)
		return 1
	}
	switch control {
	case "help":
		printUsage(stdout)
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(rest) == 0 {
		printUsage(stderr)
		return 1
	}

	client := NewClient(ClientConfig{BaseURL: cfg.addr, APIKey: cfg.apiKey, Timeout: cfg.timeout})
	ctx := context.Background()

	switch rest[0] {
	case "errors":
		return runErrors(ctx, client, cfg, rest[1:], stdout, stderr)
	case "trace":
		return runTrace(ctx, client, cfg, rest[1:], stdout, stderr)
	case "explain":
		return runExplain(ctx, client, cfg, rest[1:], stdout, stderr)
	case "blast":
		return runBlast(ctx, client, cfg, rest[1:], stdout, stderr)
	case "search":
		return runSearch(ctx, client, cfg, rest[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", rest[0])
		printUsage(stderr)
		return 1
	}
}

func parseGlobal(args []string) (cliConfig, []string, string, error) {
	cfg := cliConfig{
		addr:    os.Getenv("INGEST_ADDR"),
		apiKey:  os.Getenv("WAYLOG_READ_KEY"),
		timeout: parseEnvDuration("WAYLOG_CLI_TIMEOUT", 5*time.Second),
	}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return cfg, nil, "help", nil
		case arg == "--version":
			return cfg, nil, "version", nil
		case arg == "--json":
			cfg.json = true
		case arg == "--addr" || arg == "--api-key" || arg == "--timeout":
			if i+1 >= len(args) {
				return cfg, nil, "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			if err := setGlobalValue(&cfg, arg, args[i]); err != nil {
				return cfg, nil, "", err
			}
		case strings.HasPrefix(arg, "--addr="):
			if err := setGlobalValue(&cfg, "--addr", strings.TrimPrefix(arg, "--addr=")); err != nil {
				return cfg, nil, "", err
			}
		case strings.HasPrefix(arg, "--api-key="):
			if err := setGlobalValue(&cfg, "--api-key", strings.TrimPrefix(arg, "--api-key=")); err != nil {
				return cfg, nil, "", err
			}
		case strings.HasPrefix(arg, "--timeout="):
			if err := setGlobalValue(&cfg, "--timeout", strings.TrimPrefix(arg, "--timeout=")); err != nil {
				return cfg, nil, "", err
			}
		default:
			rest = append(rest, arg)
		}
	}
	return cfg, rest, "", nil
}

func setGlobalValue(cfg *cliConfig, key, value string) error {
	switch key {
	case "--addr":
		cfg.addr = value
	case "--api-key":
		cfg.apiKey = value
	case "--timeout":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid timeout: %s", value)
		}
		cfg.timeout = d
	}
	return nil
}

func requireV2Reads(ctx context.Context, client *Client, stderr io.Writer) int {
	caps, err := client.Capabilities(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "capability check failed: %v\n", err)
		return exitCodeForError(err)
	}
	if !caps.V2Reads.Enabled {
		fmt.Fprintln(stderr, "server must run with WAYLOG_V2_READS=true for the v2 CLI")
		return 3
	}
	return 0
}

func runErrors(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("errors", stderr)
	window := fs.String("window", "", "")
	service := fs.String("service", "", "")
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage(stderr, "usage: waylog errors [--window <dur>] [--service <svc>] [--limit <n>] [--cursor <c>] [--json]")
	}
	if gate := requireV2Reads(ctx, client, stderr); gate != 0 {
		return gate
	}
	resp, err := client.Errors(ctx, ErrorsParams{Window: *window, Service: *service, Limit: *limit, Cursor: *cursor})
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderErrors)
}

func runTrace(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usage(stderr, "usage: waylog trace <trace_id> [--json]")
	}
	if gate := requireV2Reads(ctx, client, stderr); gate != 0 {
		return gate
	}
	resp, err := client.Trace(ctx, args[0])
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderTrace)
}

func runExplain(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usage(stderr, "usage: waylog explain <event_id|trace_id> [--json]")
	}
	if gate := requireV2Reads(ctx, client, stderr); gate != 0 {
		return gate
	}
	id := args[0]
	resp, err := client.Story(ctx, StoryQuery{EventID: id})
	if isNotFound(err) {
		resp, err = client.Story(ctx, StoryQuery{TraceID: id})
	}
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderStory)
}

func runBlast(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("blast", stderr)
	service := fs.String("service", "", "")
	step := fs.String("step", "", "")
	errorCode := fs.String("code", "", "")
	window := fs.String("window", "", "")
	if err := fs.Parse(args); err != nil {
		return usage(stderr, "usage: waylog blast (--service <svc> --step <step> --code <code> | --code <code> | <service:step:code>) [--window <dur>] [--json]")
	}
	p, err := resolveBlastForm(BlastParams{Service: *service, Step: *step, ErrorCode: *errorCode, Window: *window}, fs.Args())
	if err != nil {
		return usage(stderr, err.Error())
	}
	if gate := requireV2Reads(ctx, client, stderr); gate != 0 {
		return gate
	}
	resp, err := client.Blast(ctx, p)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderBlast)
}

func resolveBlastForm(flags BlastParams, positional []string) (BlastParams, error) {
	if len(positional) > 1 {
		return BlastParams{}, errors.New("usage: waylog blast (--service <svc> --step <step> --code <code> | --code <code> | <service:step:code>) [--window <dur>] [--json]")
	}
	if len(positional) == 1 {
		if flags.Service != "" || flags.Step != "" || flags.ErrorCode != "" {
			return BlastParams{}, errors.New("display error family cannot be combined with --service, --step, or --code")
		}
		flags.ErrorFamily = positional[0]
		return flags, nil
	}
	if flags.Service != "" || flags.Step != "" {
		if flags.Service == "" || flags.Step == "" || flags.ErrorCode == "" {
			return BlastParams{}, errors.New("--service, --step, and --code must be supplied together")
		}
		return flags, nil
	}
	if flags.ErrorCode != "" {
		return flags, nil
	}
	return BlastParams{}, errors.New("blast requires --code or a display error family")
}

func runSearch(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("search", stderr)
	service := fs.String("service", "", "")
	status := fs.String("status", "", "")
	window := fs.String("window", "", "")
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	errorCode := fs.String("error-code", "", "")
	traceID := fs.String("trace-id", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 1 {
		return usage(stderr, "usage: waylog search <query> [--service <svc>] [--status <csv>] [--window <dur>] [--limit <n>] [--cursor <c>] [--json]")
	}
	query := ""
	if fs.NArg() == 1 {
		query = fs.Arg(0)
	}
	p := SearchParams{Service: *service, Status: *status, Window: *window, Limit: *limit, Cursor: *cursor, ErrorCode: *errorCode, TraceID: *traceID}
	if p.ErrorCode == "" && p.TraceID == "" {
		if query == "" {
			return usage(stderr, "search requires <query>, --error-code, or --trace-id")
		}
		p.ErrorCode = query
	}
	if gate := requireV2Reads(ctx, client, stderr); gate != 0 {
		return gate
	}
	resp, err := client.Search(ctx, p)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderSearch)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func renderOrError[T any](stdout, stderr io.Writer, asJSON bool, resp T, err error, render func(io.Writer, T)) int {
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitCodeForError(err)
	}
	if asJSON {
		if err := renderJSON(stdout, resp); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	render(stdout, resp)
	return 0
}

func isNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == "not_found"
}

func usage(stderr io.Writer, msg string) int {
	fmt.Fprintln(stderr, msg)
	return 1
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  waylog errors [--window <dur>] [--service <svc>] [--limit <n>] [--cursor <c>] [--json]
  waylog trace <trace_id> [--json]
  waylog explain <event_id|trace_id> [--json]
  waylog blast (--service <svc> --step <step> --code <code> | --code <code> | <service:step:code>) [--window <dur>] [--json]
  waylog search <query> [--service <svc>] [--status <csv>] [--window <dur>] [--limit <n>] [--cursor <c>] [--json]

Global flags:
  --addr <url>       ingest base URL (default INGEST_ADDR or http://localhost:8080)
  --api-key <key>    read API key (default WAYLOG_READ_KEY)
  --timeout <dur>    HTTP timeout (default WAYLOG_CLI_TIMEOUT or 5s)
  --json             pretty-print raw JSON response
  --version          print version`)
}
