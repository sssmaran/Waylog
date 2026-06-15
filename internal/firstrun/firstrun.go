package firstrun

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Options configures the first-run experience.
type Options struct {
	Requests int           // failing requests to emit (default 25)
	Timeout  time.Duration // max wait for an incident (default 90s)
	Stdout   io.Writer
	Stderr   io.Writer
	NoWait   bool // if true, tear down after printing the report (tests/CI)
}

// Run executes the full first-run: launch ingest, drive a burst, wait for the
// incident, print the deterministic report, then (unless NoWait) keep the
// server up so the operator can browse the dashboard and run `crux ...`.
func Run(opt Options) error {
	if opt.Requests <= 0 {
		opt.Requests = 25
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 90 * time.Second
	}
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}

	dataDir, err := os.MkdirTemp("", "crux-first-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dataDir)

	port, err := freePort()
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ingestURL := "http://" + addr
	const writeKey = "demo"

	execPath, _ := os.Executable()
	execDir := filepath.Dir(execPath)
	cmdPath, cmdArgs, runDir, err := locateServer(execDir)
	if err != nil {
		return err
	}

	fmt.Fprintf(opt.Stdout, "Starting Crux on %s ...\n", ingestURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srvCmd := exec.CommandContext(ctx, cmdPath, cmdArgs...)
	srvCmd.Env = serverEnv(dataDir, writeKey, addr)
	if runDir != "" {
		srvCmd.Dir = runDir
	}
	srvCmd.Stdout = io.Discard
	srvCmd.Stderr = io.Discard
	// Run the server in its own process group. With `go run ./cmd/ingest`, the go
	// process forks a child (the compiled binary); cancelling the context only
	// kills `go run`, orphaning the child. Killing the whole group on teardown
	// reaps the actual ingest server too, so it doesn't leak ports/DB handles.
	srvCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := srvCmd.Start(); err != nil {
		return fmt.Errorf("start ingest: %w", err)
	}
	defer func() {
		if srvCmd.Process != nil {
			_ = syscall.Kill(-srvCmd.Process.Pid, syscall.SIGKILL)
		}
		cancel()
		_ = srvCmd.Wait()
	}()

	if err := waitReady(ingestURL+"/readyz", 30*time.Second); err != nil {
		return fmt.Errorf("ingest did not become ready: %w", err)
	}

	fmt.Fprintf(opt.Stdout, "Generating a checkout->payment failure burst (%d requests) ...\n", opt.Requests)
	if _, err := RunBurst(BurstConfig{IngestURL: ingestURL, WriteKey: writeKey, Requests: opt.Requests}); err != nil {
		return fmt.Errorf("burst: %w", err)
	}

	fmt.Fprintln(opt.Stdout, "Waiting for the incident engine to open an incident ...")
	rep, err := waitForReport(reportPoll{IngestURL: ingestURL, ReadKey: "", Timeout: opt.Timeout, Interval: time.Second})
	if err != nil {
		return err
	}

	printReport(opt.Stdout, ingestURL, rep)

	if opt.NoWait {
		return nil
	}
	fmt.Fprintf(opt.Stdout, "\nServer running at %s -- open %s/ui/ in a browser.\nPress Ctrl-C to stop.\n", ingestURL, ingestURL)
	sig := waitForInterrupt()
	<-sig
	return nil
}

func printReport(w io.Writer, ingestURL string, rep reportResult) {
	bar := "============================================================"
	fmt.Fprintf(w, "\n%s\n", bar)
	fmt.Fprintf(w, "  Incident %s -- deterministic triage report\n", rep.IncidentID)
	fmt.Fprintf(w, "  report_hash: %s\n", rep.ReportHash)
	fmt.Fprintf(w, "%s\n\n", bar)
	fmt.Fprintln(w, rep.Markdown)
	fmt.Fprintf(w, "\nNext (run `crux incidents` against this server):\n")
	fmt.Fprintf(w, "  crux --addr %s incidents\n", ingestURL)
	fmt.Fprintf(w, "  crux --addr %s triage %s --snapshot\n", ingestURL, rep.IncidentID)
	fmt.Fprintf(w, "  open %s/ui/\n", ingestURL)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, code, err := getWithKey(url, ""); err == nil && code == 200 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", url)
}

func waitForInterrupt() <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch
}
