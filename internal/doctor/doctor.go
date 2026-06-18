// Package doctor runs read-only health checks for a Waylog/Crux deployment and
// reports them as a green/red checklist. It is read-only except for one
// transient temp-file probe in the WAL dir (see checkWALDir).
package doctor

import (
	"os"
	"strings"
)

type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Check is a single diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Result is the full set of checks from one doctor run.
type Result struct {
	Checks []Check `json:"checks"`
}

// OK reports whether no check failed. Warn and skip do not fail the run.
func (r Result) OK() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// Options controls which checks run.
type Options struct {
	Addr         string // base URL for server checks (e.g. http://localhost:8080)
	ServerChecks bool   // run reachability checks against Addr
}

// EnvKeys are the environment variables doctor reads for its local checks.
// Exported so tests can clear them all in one place, keeping the test fixtures
// from drifting out of sync with processEnv.
var EnvKeys = []string{
	"WAYLOG_PROFILE", "WAYLOG_API_KEY", "WAYLOG_WRITE_KEY", "WAYLOG_READ_KEY",
	"WAYLOG_AGENT_KEY", "DASHBOARD_AUTH", "DASHBOARD_SESSION_SECRET",
	"EVENT_LOG_V2_DIR", "EVENT_LOG_DIR", "SQLITE_PATH",
}

// processEnv snapshots the environment variables the checks read.
func processEnv() map[string]string {
	env := make(map[string]string, len(EnvKeys))
	for _, k := range EnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	return env
}

// Run executes the local checks always and the server checks when requested.
func Run(opts Options) Result {
	env := processEnv()
	checks := []Check{
		checkAuth(env),
		checkWALDir(env),
		checkSQLite(env),
		checkTriageHash(),
	}
	if opts.ServerChecks {
		addr := strings.TrimSpace(opts.Addr)
		if addr == "" {
			addr = "http://localhost:8080"
		}
		checks = append(checks, checkServer(addr)...)
	}
	return Result{Checks: checks}
}
