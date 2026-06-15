package firstrun

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// serverEnv returns the environment for the embedded demo ingest server. It
// mirrors scripts/demo.sh's no-login, fast-tick local profile and points all
// state at the throwaway dir. SQLITE_PATH + EVENT_LOG_DIR are mandatory: the
// incident engine only enables when SQLite is configured (cmd/ingest/main.go).
func serverEnv(dataDir, writeKey, addr string) []string {
	base := os.Environ()
	set := map[string]string{
		"INGEST_ADDR":                   addr,
		"WAYLOG_WRITE_KEY":              writeKey,
		"WAYLOG_READ_KEY":               "",
		"DASHBOARD_AUTH":                "off",
		"WAYLOG_PROFILE":                "demo",
		"WAYLOG_INCIDENT_TICK_INTERVAL": "5s",
		"WAYLOG_INCIDENTS_ENABLED":      "true",
		// Disable the OTLP gRPC listener: it binds a fixed port (:4317) that
		// collides if anything else is already running, crashing the process at
		// boot. The first-run burst uses the HTTP ingest path, which is unaffected.
		"OTLP_ENABLED":     "false",
		"SQLITE_PATH":      filepath.Join(dataDir, "crux.db"),
		"EVENT_LOG_DIR":    filepath.Join(dataDir, "eventlog"),
		"EVENT_LOG_V2_DIR": filepath.Join(dataDir, "eventlog-v2"),
		"CORS_ORIGIN":      "*",
	}
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		keep := true
		for k := range set {
			if len(kv) > len(k) && kv[:len(k)+1] == k+"=" {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// locateServer finds the ingest server to launch. It returns the command, its
// args, and the working directory to spawn it in ("" inherits the current dir).
// Preference order:
//  1. an `ingest` binary adjacent to the running crux executable (release tier),
//  2. `go run ./cmd/ingest` when a source checkout + Go toolchain are present,
//     discovered by walking up from the current working directory to the Go
//     module root (the dir containing go.mod). This is CWD-independent: it works
//     from any subdirectory of the checkout (e.g. tests run from internal/firstrun).
func locateServer(execDir string) (cmd string, args []string, dir string, err error) {
	adjacent := filepath.Join(execDir, "ingest")
	if fi, statErr := os.Stat(adjacent); statErr == nil && !fi.IsDir() {
		return adjacent, nil, "", nil
	}
	if goBin, lookErr := exec.LookPath("go"); lookErr == nil {
		if root := moduleRoot(); root != "" {
			if fi, statErr := os.Stat(filepath.Join(root, "cmd", "ingest")); statErr == nil && fi.IsDir() {
				return goBin, []string{"run", "./cmd/ingest"}, root, nil
			}
		}
	}
	return "", nil, "", fmt.Errorf("no ingest server found: expected %q next to crux, or run from a source checkout with Go installed", adjacent)
}

// moduleRoot walks up from the current working directory looking for a go.mod
// file and returns the directory containing it, or "" if none is found.
func moduleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if fi, statErr := os.Stat(filepath.Join(wd, "go.mod")); statErr == nil && !fi.IsDir() {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return ""
		}
		wd = parent
	}
}
