package doctor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/auth"
	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	eventlogv2 "github.com/sssmaran/WaylogCLI/internal/eventlog/v2"
	_ "modernc.org/sqlite"
)

// checkAuth runs the auth/config constraint matrix and surfaces weak keys.
// A constraint error fails the run; weak placeholder keys only warn.
func checkAuth(env map[string]string) Check {
	cfg, err := auth.ParseConfig(env)
	if err != nil {
		return Check{Name: "auth/config", Status: StatusFail, Detail: err.Error()}
	}
	if warns := cfg.WeakKeyWarnings(); len(warns) > 0 {
		return Check{Name: "auth/config", Status: StatusWarn, Detail: strings.Join(warns, "; ")}
	}
	return Check{Name: "auth/config", Status: StatusOK, Detail: "profile=" + cfg.Profile}
}

// checkWALDir verifies the server can write its WAL. It resolves the dir exactly
// like the server (eventlogv2.ResolveDir — never "", so the check never skips),
// then — because the server MkdirAll's the dir on startup — probes writability
// of the dir, or of the nearest existing ancestor when the dir doesn't exist yet
// (so a not-yet-created default path passes when it is creatable). The probe is
// a single temp file, immediately removed — doctor's only write; a probe file
// that cannot be removed is surfaced as a warning.
func checkWALDir(env map[string]string) Check {
	dir := eventlogv2.ResolveDir(env["EVENT_LOG_V2_DIR"], env["EVENT_LOG_DIR"])
	target, info := nearestExistingDir(dir)
	if target == "" {
		return Check{Name: "wal-dir", Status: StatusFail, Detail: dir + ": no existing parent directory"}
	}
	if !info.IsDir() {
		return Check{Name: "wal-dir", Status: StatusFail, Detail: target + ": not a directory"}
	}
	writable, leaked, err := probeWritable(target)
	if !writable {
		return Check{Name: "wal-dir", Status: StatusFail, Detail: fmt.Sprintf("%s: not writable: %v", target, err)}
	}
	if leaked != "" {
		return Check{Name: "wal-dir", Status: StatusWarn, Detail: fmt.Sprintf("%s writable but probe file %s could not be removed: %v", target, leaked, err)}
	}
	if target != dir {
		return Check{Name: "wal-dir", Status: StatusOK, Detail: fmt.Sprintf("%s absent; will be created (parent %s writable)", dir, target)}
	}
	return Check{Name: "wal-dir", Status: StatusOK, Detail: dir + " (writable)"}
}

// nearestExistingDir walks up from dir to the first path that exists on disk,
// returning it with its FileInfo, or ("", nil) if none is found. It is a
// string-only walk (filepath.Dir) and does not resolve symlinks, so it is an
// approximation of the server's os.MkdirAll — the server's call is the
// authoritative test.
func nearestExistingDir(dir string) (string, os.FileInfo) {
	for dir != "" {
		if info, err := os.Stat(dir); err == nil {
			return dir, info
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
	return "", nil
}

// probeWritable proves dir is writable with a transient temp file. writable is
// true when the file was created; leaked is the temp path if it could not be
// removed afterward (doctor's only side effect was left behind).
func probeWritable(dir string) (writable bool, leaked string, err error) {
	f, err := os.CreateTemp(dir, ".waylog-doctor-*")
	if err != nil {
		return false, "", err
	}
	name := f.Name()
	_ = f.Close()
	if rmErr := os.Remove(name); rmErr != nil {
		return true, name, rmErr
	}
	return true, "", nil
}

// checkSQLite opens the cold store read-only and reports migration state. It
// never creates the database and never applies migrations.
func checkSQLite(env map[string]string) Check {
	path := strings.TrimSpace(env["SQLITE_PATH"])
	if path == "" {
		return Check{Name: "sqlite", Status: StatusSkip, Detail: "SQLITE_PATH unset (cold store disabled)"}
	}
	if _, err := os.Stat(path); err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("%s: %v (doctor does not create it)", path, err)}
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_busy_timeout=2000")
	if err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("open read-only: %v", err)}
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("ping: %v", err)}
	}

	applied := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("read schema_migrations: %v", err)}
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("scan migration: %v", err)}
		}
		applied[n] = true
	}
	if err := rows.Err(); err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("iterate migrations: %v", err)}
	}

	expected, err := coldstore.MigrationNames()
	if err != nil {
		return Check{Name: "sqlite", Status: StatusFail, Detail: err.Error()}
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, name := range expected {
		expectedSet[name] = true
	}
	// Behind (DB missing migrations this binary expects) is a hard failure: the
	// binary's queries reference schema that isn't there.
	var missing []string
	for _, name := range expected {
		if !applied[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Check{Name: "sqlite", Status: StatusFail, Detail: fmt.Sprintf("%d migration(s) behind — cold store not ready for this binary: %s", len(missing), strings.Join(missing, ", "))}
	}
	// Ahead (DB has migrations this binary doesn't know — a newer binary wrote
	// it) is a caution, not a hard failure: reads of known schema still work.
	var unknown []string
	for name := range applied {
		if !expectedSet[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Check{Name: "sqlite", Status: StatusWarn, Detail: fmt.Sprintf("DB has %d migration(s) unknown to this binary (newer binary wrote it): %s", len(unknown), strings.Join(unknown, ", "))}
	}
	return Check{Name: "sqlite", Status: StatusOK, Detail: fmt.Sprintf("%s (%d migrations applied)", filepath.Base(path), len(expected))}
}

// checkServer probes liveness/readiness endpoints. Used only when server checks
// are requested; a dead address fails (it never silently passes).
func checkServer(addr string) []Check {
	base := strings.TrimRight(addr, "/")
	client := &http.Client{Timeout: 3 * time.Second}
	probe := func(name, path string) Check {
		resp, err := client.Get(base + path)
		if err != nil {
			return Check{Name: name, Status: StatusFail, Detail: err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return Check{Name: name, Status: StatusFail, Detail: fmt.Sprintf("%s -> %d", path, resp.StatusCode)}
		}
		return Check{Name: name, Status: StatusOK, Detail: base + path}
	}
	return []Check{
		probe("server-livez", "/livez"),
		probe("server-readyz", "/readyz"),
		probeHealth(client, base),
	}
}

// probeHealth inspects /healthz for a degraded state — e.g. a failed WAL replay
// that leaves the server "ready" (so /readyz passes) but serving partial reads.
// /healthz always returns 200 with a JSON status; a non-"ok" status is a warning.
func probeHealth(client *http.Client, base string) Check {
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return Check{Name: "server-health", Status: StatusFail, Detail: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Check{Name: "server-health", Status: StatusFail, Detail: fmt.Sprintf("/healthz -> %d", resp.StatusCode)}
	}
	var body struct {
		Status string `json:"status"`
		Replay struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"replay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Check{Name: "server-health", Status: StatusWarn, Detail: fmt.Sprintf("/healthz returned 200 but body was undecodable: %v", err)}
	}
	if body.Status == "" {
		return Check{Name: "server-health", Status: StatusWarn, Detail: "/healthz returned 200 with no status field (unexpected response — not a Waylog server?)"}
	}
	if body.Status != "ok" {
		detail := "status=" + body.Status
		switch {
		case body.Replay.Status != "" && body.Replay.Error != "":
			detail = fmt.Sprintf("status=%s (replay=%s: %s)", body.Status, body.Replay.Status, body.Replay.Error)
		case body.Replay.Status != "":
			detail = fmt.Sprintf("status=%s (replay=%s)", body.Status, body.Replay.Status)
		}
		return Check{Name: "server-health", Status: StatusWarn, Detail: detail}
	}
	return Check{Name: "server-health", Status: StatusOK, Detail: base + "/healthz (status=ok)"}
}
