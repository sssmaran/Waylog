package firstrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerEnvEnablesIncidentsAndIsLocalOnly(t *testing.T) {
	dir := t.TempDir()
	env := serverEnv(dir, "demo", "127.0.0.1:8099")

	get := func(k string) string {
		for _, kv := range env {
			if strings.HasPrefix(kv, k+"=") {
				return strings.TrimPrefix(kv, k+"=")
			}
		}
		return ""
	}
	if get("SQLITE_PATH") == "" {
		t.Fatal("SQLITE_PATH must be set so the incident engine is enabled")
	}
	if get("EVENT_LOG_DIR") == "" {
		t.Fatal("EVENT_LOG_DIR must be set for durable ingest + incidents")
	}
	if get("WAYLOG_INCIDENT_TICK_INTERVAL") != "5s" {
		t.Fatalf("tick interval = %q, want 5s for a fast demo", get("WAYLOG_INCIDENT_TICK_INTERVAL"))
	}
	if get("DASHBOARD_AUTH") != "off" {
		t.Fatalf("DASHBOARD_AUTH = %q, want off for no-login demo", get("DASHBOARD_AUTH"))
	}
	if !strings.HasPrefix(get("INGEST_ADDR"), "127.0.0.1:") {
		t.Fatalf("INGEST_ADDR = %q, want loopback-bound", get("INGEST_ADDR"))
	}
	if !strings.HasPrefix(get("SQLITE_PATH"), dir) {
		t.Fatal("SQLITE_PATH must live under the throwaway dir")
	}
}

func TestLocateServerPrefersAdjacentIngestBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ingest")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, args, runDir, err := locateServer(dir)
	if err != nil {
		t.Fatalf("locateServer: %v", err)
	}
	if cmd != bin || len(args) != 0 || runDir != "" {
		t.Fatalf("got cmd=%q args=%v dir=%q, want adjacent ingest binary with empty dir", cmd, args, runDir)
	}
}

func TestLocateServerFindsModuleRootFromSubdir(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required")
	}
	// Build a fake module: <root>/go.mod, <root>/cmd/ingest/, <root>/sub/.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fake\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "ingest"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	// execDir has no adjacent ingest binary, forcing the go-run fallback.
	cmd, args, runDir, err := locateServer(t.TempDir())
	if err != nil {
		t.Fatalf("locateServer: %v", err)
	}
	if len(args) != 2 || args[0] != "run" || args[1] != "./cmd/ingest" {
		t.Fatalf("got cmd=%q args=%v, want go run ./cmd/ingest", cmd, args)
	}
	// t.TempDir on macOS may resolve through /private symlinks; compare by EvalSymlinks.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(runDir)
	if gotRoot != wantRoot {
		t.Fatalf("runDir = %q, want module root %q", runDir, root)
	}
}
