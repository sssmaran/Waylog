package cliv2

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const version = "v2.1-triage"

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
	case "capabilities":
		return runCapabilities(ctx, client, cfg, rest[1:], stdout, stderr)
	case "recent":
		return runRecent(ctx, client, cfg, rest[1:], stdout, stderr)
	case "incidents":
		return runIncidents(ctx, client, cfg, rest[1:], stdout, stderr)
	case "incident":
		return runIncident(ctx, client, cfg, rest[1:], stdout, stderr)
	case "errors":
		return runErrors(ctx, client, cfg, rest[1:], stdout, stderr)
	case "event":
		return runEvent(ctx, client, cfg, rest[1:], stdout, stderr)
	case "trace":
		return runTrace(ctx, client, cfg, rest[1:], stdout, stderr)
	case "explain":
		return runExplain(ctx, client, cfg, rest[1:], stdout, stderr)
	case "blast":
		return runBlast(ctx, client, cfg, rest[1:], stdout, stderr)
	case "search":
		return runSearch(ctx, client, cfg, rest[1:], stdout, stderr)
	case "triage":
		return runTriage(ctx, client, cfg, rest[1:], stdout, stderr)
	case "doctor":
		return runDoctor(cfg, rest[1:], stdout, stderr)
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

func runCapabilities(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return usage(stderr, "usage: waylog capabilities [--json]")
	}
	resp, err := client.Capabilities(ctx)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderCapabilities)
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
	resp, err := client.Errors(ctx, ErrorsParams{Window: *window, Service: *service, Limit: *limit, Cursor: *cursor})
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderErrors)
}

func runRecent(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("recent", stderr)
	window := fs.String("window", "", "")
	service := fs.String("service", "", "")
	status := fs.String("status", "", "")
	limit := fs.Int("limit", 0, "")
	cursor := fs.String("cursor", "", "")
	includeSuppressed := fs.Bool("include-suppressed", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage(stderr, "usage: waylog recent [--window <dur>] [--service <svc>] [--status <csv>] [--limit <n>] [--cursor <c>] [--include-suppressed] [--json]")
	}
	resp, err := client.Recent(ctx, RecentParams{Window: *window, Service: *service, Status: *status, Limit: *limit, Cursor: *cursor, IncludeSuppressed: *includeSuppressed})
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderRecent)
}

func runIncidents(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return usage(stderr, "usage: waylog incidents [--json]")
	}
	resp, err := client.Incidents(ctx)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderIncidents)
}

func runIncident(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	incidentID, snapshot, err := parseIncidentArgs(args)
	if err != nil {
		return usage(stderr, err.Error())
	}
	if snapshot {
		if cfg.json {
			resp, err := client.IncidentSnapshotJSON(ctx, incidentID)
			return renderOrError(stdout, stderr, true, resp, err, RenderIncidentSnapshot)
		}
		text, err := client.IncidentSnapshotText(ctx, incidentID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitCodeForError(err)
		}
		fmt.Fprint(stdout, text)
		return 0
	}
	resp, err := client.Incident(ctx, incidentID)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderIncident)
}

func parseIncidentArgs(args []string) (string, bool, error) {
	incidentID := ""
	snapshot := false
	for _, arg := range args {
		switch {
		case arg == "--snapshot":
			snapshot = true
		case strings.HasPrefix(arg, "-"):
			return "", false, fmt.Errorf("unknown flag: %s", arg)
		default:
			if incidentID != "" {
				return "", false, errors.New("usage: waylog incident <incident_id> [--snapshot] [--json]")
			}
			incidentID = arg
		}
	}
	if incidentID == "" {
		return "", false, errors.New("usage: waylog incident <incident_id> [--snapshot] [--json]")
	}
	return incidentID, snapshot, nil
}

func runTriage(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	id, window, snapshot, err := parseTriageArgs(args)
	if err != nil {
		return usage(stderr, err.Error())
	}
	rep, err := client.Triage(ctx, id, TriageParams{Window: window, Snapshot: snapshot})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitCodeForError(err)
	}
	if cfg.json {
		if err := renderJSON(stdout, rep); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	return RenderTriage(stdout, rep)
}

func parseTriageArgs(args []string) (id, window string, snapshot bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--snapshot":
			snapshot = true
		case arg == "--window":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("--window requires a value")
			}
			window = args[i+1]
			i++
		case strings.HasPrefix(arg, "--window="):
			window = strings.TrimPrefix(arg, "--window=")
		case strings.HasPrefix(arg, "-"):
			return "", "", false, fmt.Errorf("unknown flag: %s", arg)
		case id == "":
			id = arg
		default:
			return "", "", false, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	if id == "" {
		return "", "", false, fmt.Errorf("usage: waylog triage <incident_id> [--window 15m] [--snapshot]")
	}
	return id, window, snapshot, nil
}

func runEvent(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usage(stderr, "usage: waylog event <event_id> [--json]")
	}
	resp, err := client.Event(ctx, args[0])
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderEvent)
}

func runTrace(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usage(stderr, "usage: waylog trace <trace_id> [--json]")
	}
	resp, err := client.Trace(ctx, args[0])
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderTrace)
}

func runExplain(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		return usage(stderr, "usage: waylog explain <event_id|trace_id> [--json]")
	}
	id := args[0]
	resp, err := client.Story(ctx, StoryQuery{EventID: id})
	if isNotFound(err) {
		resp, err = client.Story(ctx, StoryQuery{TraceID: id})
	}
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderStory)
}

func runBlast(ctx context.Context, client *Client, cfg cliConfig, args []string, stdout, stderr io.Writer) int {
	p, err := parseBlastArgs(args)
	if err != nil {
		return usage(stderr, err.Error())
	}
	resp, err := client.Blast(ctx, p)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderBlast)
}

func parseBlastArgs(args []string) (BlastParams, error) {
	var flags BlastParams
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--service" || arg == "--step" || arg == "--code" || arg == "--window":
			if i+1 >= len(args) {
				return BlastParams{}, fmt.Errorf("%s requires a value", arg)
			}
			i++
			setBlastFlag(&flags, arg, args[i])
		case strings.HasPrefix(arg, "--service="):
			setBlastFlag(&flags, "--service", strings.TrimPrefix(arg, "--service="))
		case strings.HasPrefix(arg, "--step="):
			setBlastFlag(&flags, "--step", strings.TrimPrefix(arg, "--step="))
		case strings.HasPrefix(arg, "--code="):
			setBlastFlag(&flags, "--code", strings.TrimPrefix(arg, "--code="))
		case strings.HasPrefix(arg, "--window="):
			setBlastFlag(&flags, "--window", strings.TrimPrefix(arg, "--window="))
		case strings.HasPrefix(arg, "-"):
			return BlastParams{}, fmt.Errorf("unknown flag: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	return resolveBlastForm(flags, positionals)
}

func setBlastFlag(p *BlastParams, key, value string) {
	switch key {
	case "--service":
		p.Service = value
	case "--step":
		p.Step = value
	case "--code":
		p.ErrorCode = value
	case "--window":
		p.Window = value
	}
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
	p, query, err := parseSearchArgs(args)
	if err != nil {
		return usage(stderr, err.Error())
	}
	if p.ErrorCode == "" && p.TraceID == "" {
		if query == "" {
			return usage(stderr, "search requires <query>, --error-code, or --trace-id")
		}
		p.ErrorCode = query
	}
	resp, err := client.Search(ctx, p)
	return renderOrError(stdout, stderr, cfg.json, resp, err, RenderSearch)
}

func parseSearchArgs(args []string) (SearchParams, string, error) {
	var p SearchParams
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--service" || arg == "--status" || arg == "--window" || arg == "--cursor" || arg == "--error-code" || arg == "--trace-id":
			if i+1 >= len(args) {
				return SearchParams{}, "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			setSearchStringFlag(&p, arg, args[i])
		case arg == "--limit":
			if i+1 >= len(args) {
				return SearchParams{}, "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			limit, err := strconv.Atoi(args[i])
			if err != nil {
				return SearchParams{}, "", fmt.Errorf("invalid limit: %s", args[i])
			}
			p.Limit = limit
		case strings.HasPrefix(arg, "--service="):
			setSearchStringFlag(&p, "--service", strings.TrimPrefix(arg, "--service="))
		case strings.HasPrefix(arg, "--status="):
			setSearchStringFlag(&p, "--status", strings.TrimPrefix(arg, "--status="))
		case strings.HasPrefix(arg, "--window="):
			setSearchStringFlag(&p, "--window", strings.TrimPrefix(arg, "--window="))
		case strings.HasPrefix(arg, "--cursor="):
			setSearchStringFlag(&p, "--cursor", strings.TrimPrefix(arg, "--cursor="))
		case strings.HasPrefix(arg, "--error-code="):
			setSearchStringFlag(&p, "--error-code", strings.TrimPrefix(arg, "--error-code="))
		case strings.HasPrefix(arg, "--trace-id="):
			setSearchStringFlag(&p, "--trace-id", strings.TrimPrefix(arg, "--trace-id="))
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return SearchParams{}, "", fmt.Errorf("invalid limit: %s", strings.TrimPrefix(arg, "--limit="))
			}
			p.Limit = limit
		case strings.HasPrefix(arg, "-"):
			return SearchParams{}, "", fmt.Errorf("unknown flag: %s", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return SearchParams{}, "", errors.New("usage: waylog search <query> [--service <svc>] [--status <csv>] [--window <dur>] [--limit <n>] [--cursor <c>] [--json]")
	}
	query := ""
	if len(positionals) == 1 {
		query = positionals[0]
	}
	return p, query, nil
}

func setSearchStringFlag(p *SearchParams, key, value string) {
	switch key {
	case "--service":
		p.Service = value
	case "--status":
		p.Status = value
	case "--window":
		p.Window = value
	case "--cursor":
		p.Cursor = value
	case "--error-code":
		p.ErrorCode = value
	case "--trace-id":
		p.TraceID = value
	}
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
  waylog capabilities [--json]
  waylog recent [--window <dur>] [--service <svc>] [--status <csv>] [--limit <n>] [--cursor <c>] [--include-suppressed] [--json]
  waylog incidents [--json]
  waylog incident <incident_id> [--snapshot] [--json]
  waylog errors [--window <dur>] [--service <svc>] [--limit <n>] [--cursor <c>] [--json]
  waylog event <event_id> [--json]
  waylog trace <trace_id> [--json]
  waylog explain <event_id|trace_id> [--json]
  waylog blast (--service <svc> --step <step> --code <code> | --code <code> | <service:step:code>) [--window <dur>] [--json]
  waylog search <query> [--service <svc>] [--status <csv>] [--window <dur>] [--limit <n>] [--cursor <c>] [--json]
  waylog doctor [--server] [--json]

Recommended loop:
  waylog incidents
  waylog incident <incident_id>
  waylog recent
  waylog errors --window 15m
  waylog blast checkout:payment.charge:PMT_502 --window 15m
  waylog explain <trace_id>

Global flags:
  --addr <url>       ingest base URL (default INGEST_ADDR or http://localhost:8080)
  --api-key <key>    read API key (default WAYLOG_READ_KEY)
  --timeout <dur>    HTTP timeout (default WAYLOG_CLI_TIMEOUT or 5s)
  --json             pretty-print raw JSON response
  --version          print version`)
}
