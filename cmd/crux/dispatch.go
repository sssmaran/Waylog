package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	cliv2 "github.com/sssmaran/WaylogCLI/internal/cli/v2"
)

type ResultKind int

const (
	ResultOK ResultKind = iota
	ResultNoop
	ResultExit
	ResultUnknown
	ResultUsage
	ResultError
)

type Result struct {
	Kind     ResultKind
	ExitCode int
}

type runCLIFunc func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

type Dispatcher struct {
	ingestURL   string
	globalArgs  []string
	stdin       io.Reader
	runCLI      runCLIFunc
	openBrowser func(url string) error
}

func NewDispatcher(ingestURL string, globalArgs []string, stdin io.Reader) *Dispatcher {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	return &Dispatcher{
		ingestURL:   normalizeIngestURL(ingestURL),
		globalArgs:  append([]string(nil), globalArgs...),
		stdin:       stdin,
		runCLI:      cliv2.RunCLI,
		openBrowser: openBrowser,
	}
}

func parseLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case unicode.IsSpace(r) && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (d *Dispatcher) Dispatch(line string, stdout, stderr io.Writer) Result {
	tokens := parseLine(line)
	return d.DispatchTokens(tokens, stdout, stderr)
}

func (d *Dispatcher) DispatchTokens(tokens []string, stdout, stderr io.Writer) Result {
	if len(tokens) == 0 {
		return Result{Kind: ResultNoop}
	}
	cmd, rest := tokens[0], tokens[1:]
	switch cmd {
	case "exit", "quit":
		return Result{Kind: ResultExit}
	case "help":
		printHelp(stdout)
		return Result{Kind: ResultOK}
	case "open":
		return d.openIncident(rest, stdout, stderr)
	case "status", "incidents", "incident", "triage", "blast", "explain", "recent", "errors", "event", "trace", "search", "capabilities":
		args := d.cliArgs(cmd, rest)
		code := d.runCLI(args, d.stdin, stdout, stderr)
		if code != 0 {
			return Result{Kind: ResultError, ExitCode: code}
		}
		return Result{Kind: ResultOK}
	default:
		fmt.Fprintf(stderr, "unknown command: %s (type 'help' for commands)\n", cmd)
		return Result{Kind: ResultUnknown}
	}
}

func (d *Dispatcher) openIncident(args []string, stdout, stderr io.Writer) Result {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: open <incident_id>")
		return Result{Kind: ResultUsage}
	}
	target := strings.TrimRight(d.ingestURL, "/") + "/ui/#/incident/" + url.PathEscape(args[0])
	if err := d.openBrowser(target); err != nil {
		fmt.Fprintf(stderr, "open: %v\n", err)
		fmt.Fprintf(stdout, "%s\n", target)
		return Result{Kind: ResultError, ExitCode: 1}
	}
	fmt.Fprintf(stdout, "opened %s\n", target)
	return Result{Kind: ResultOK}
}

func (d *Dispatcher) cliArgs(cmd string, rest []string) []string {
	if cmd == "status" {
		cmd = "capabilities"
	}
	args := append([]string(nil), d.globalArgs...)
	args = append(args, cmd)
	args = append(args, rest...)
	return args
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  status                         show runtime capabilities")
	fmt.Fprintln(w, "  incidents                      list active incidents")
	fmt.Fprintln(w, "  incident <incident_id>         show incident detail")
	fmt.Fprintln(w, "  open <incident_id>             open incident in the dashboard")
	fmt.Fprintln(w, "  triage <incident_id>           print deterministic triage report")
	fmt.Fprintln(w, "  blast <service>:<step>:<code>  show blast radius")
	fmt.Fprintln(w, "  explain <trace_id>             explain a trace or event")
	fmt.Fprintln(w, "  recent [flags]                 show recent traces")
	fmt.Fprintln(w, "  errors [flags]                 show error families")
	fmt.Fprintln(w, "  help                           show this help")
	fmt.Fprintln(w, "  exit | quit                    leave the shell")
}
