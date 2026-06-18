package doctor

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
)

// Task 3: checkAuth tests

func TestCheckAuthFailsOnBadProfile(t *testing.T) {
	c := checkAuth(map[string]string{"WAYLOG_PROFILE": "banana"})
	if c.Status != StatusFail {
		t.Fatalf("bad profile must fail, got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckAuthOKOnDefaultEnv(t *testing.T) {
	c := checkAuth(map[string]string{}) // empty => dev profile, no keys, dashboard off
	if c.Status != StatusOK {
		t.Fatalf("default env must be ok, got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckAuthWarnsOnWeakKey(t *testing.T) {
	c := checkAuth(map[string]string{"WAYLOG_WRITE_KEY": "changeme"})
	if c.Status != StatusWarn {
		t.Fatalf("weak key must warn, got %q (%s)", c.Status, c.Detail)
	}
}

// checkWALDir tests

func TestCheckWALDirOKWhenWritable(t *testing.T) {
	dir := t.TempDir()
	c := checkWALDir(map[string]string{"EVENT_LOG_V2_DIR": dir})
	if c.Status != StatusOK {
		t.Fatalf("writable dir must be ok, got %q (%s)", c.Status, c.Detail)
	}
	// The probe must clean up after itself.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

func TestCheckWALDirOKWhenAbsentButCreatable(t *testing.T) {
	// Dir doesn't exist yet but the parent is writable: the server MkdirAll's it
	// on startup, so doctor reports ok ("will be created") rather than failing.
	absent := filepath.Join(t.TempDir(), "eventlog-v2")
	c := checkWALDir(map[string]string{"EVENT_LOG_V2_DIR": absent})
	if c.Status != StatusOK {
		t.Fatalf("absent-but-creatable dir must be ok, got %q (%s)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "will be created") {
		t.Fatalf("detail should note pending creation: %q", c.Detail)
	}
}

func TestCheckWALDirFailsWhenAncestorNotADir(t *testing.T) {
	// Nearest existing ancestor is a regular file, so creation is impossible.
	// Root-independent — it does not rely on an unwritable directory.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := checkWALDir(map[string]string{"EVENT_LOG_V2_DIR": filepath.Join(f, "v2")})
	if c.Status != StatusFail {
		t.Fatalf("ancestor-is-a-file must fail, got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckWALDirFallsBackToEventLogDirV2(t *testing.T) {
	// Only EVENT_LOG_DIR set: doctor resolves <EVENT_LOG_DIR>/v2 like the server,
	// not the bare dir.
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := checkWALDir(map[string]string{"EVENT_LOG_DIR": base})
	if c.Status != StatusOK || !strings.Contains(c.Detail, filepath.Join(base, "v2")) {
		t.Fatalf("fallback must probe <EVENT_LOG_DIR>/v2, got %q (%s)", c.Status, c.Detail)
	}
}

// Task 5: checkSQLite tests

func TestCheckSQLiteSkipsWhenUnset(t *testing.T) {
	c := checkSQLite(map[string]string{})
	if c.Status != StatusSkip {
		t.Fatalf("unset must skip, got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckSQLiteFailsWhenMissing(t *testing.T) {
	c := checkSQLite(map[string]string{"SQLITE_PATH": filepath.Join(t.TempDir(), "absent.db")})
	if c.Status != StatusFail {
		t.Fatalf("missing db must fail (must not create), got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckSQLiteOKOnMigratedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cold.db")
	// Create + migrate a real DB via coldstore, then close it.
	managed, err := coldstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = managed.Close()

	c := checkSQLite(map[string]string{"SQLITE_PATH": path})
	if c.Status != StatusOK {
		t.Fatalf("fully-migrated db must be ok, got %q (%s)", c.Status, c.Detail)
	}
}

func TestCheckSQLiteFailsWhenBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cold.db")
	managed, err := coldstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = managed.Close()
	// Test (not doctor) removes one applied migration to simulate "behind".
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	names, err := coldstore.MigrationNames()
	if err != nil {
		t.Fatalf("migration names: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE name = ?`, names[len(names)-1]); err != nil {
		t.Fatalf("delete migration: %v", err)
	}
	_ = db.Close()

	c := checkSQLite(map[string]string{"SQLITE_PATH": path})
	if c.Status != StatusFail {
		t.Fatalf("a DB behind on migrations must fail (not ready for this binary), got %q (%s)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "behind") {
		t.Fatalf("fail detail should say 'behind': %q", c.Detail)
	}
}

func TestCheckSQLiteWarnsWhenAhead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cold.db")
	managed, err := coldstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = managed.Close()
	// Test inserts a migration this binary doesn't know, as a newer binary would.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		"999_from_the_future.sql", "2099-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatalf("insert migration: %v", err)
	}
	_ = db.Close()

	c := checkSQLite(map[string]string{"SQLITE_PATH": path})
	if c.Status != StatusWarn {
		t.Fatalf("a DB ahead on migrations must warn, got %q (%s)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "999_from_the_future.sql") {
		t.Fatalf("warn detail should name the unknown migration: %q", c.Detail)
	}
}

// Task 7: checkServer tests

func TestCheckServerOKWhenHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	checks := checkServer(srv.URL)
	for _, c := range checks {
		if c.Status != StatusOK {
			t.Fatalf("%s should be ok, got %q (%s)", c.Name, c.Status, c.Detail)
		}
	}
}

func TestCheckServerWarnsWhenDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"degraded","replay":{"status":"failed","error":"wal corrupt"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	checks := checkServer(srv.URL)
	var health *Check
	for i := range checks {
		if checks[i].Name == "server-health" {
			health = &checks[i]
		} else if checks[i].Status != StatusOK {
			t.Fatalf("%s should still be ok, got %q", checks[i].Name, checks[i].Status)
		}
	}
	if health == nil {
		t.Fatal("expected a server-health check")
	}
	if health.Status != StatusWarn {
		t.Fatalf("degraded health must warn, got %q (%s)", health.Status, health.Detail)
	}
	if !strings.Contains(health.Detail, "degraded") || !strings.Contains(health.Detail, "wal corrupt") {
		t.Fatalf("degraded detail should include status + replay error: %q", health.Detail)
	}
}

func TestCheckServerFailsWhenDown(t *testing.T) {
	// Start a server, capture its URL, then close it so the address is dead but
	// still routable — more portable than relying on http://127.0.0.1:0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	checks := checkServer(url)
	if len(checks) == 0 {
		t.Fatal("expected server checks")
	}
	for _, c := range checks {
		if c.Status != StatusFail {
			t.Fatalf("%s should fail against a dead addr, got %q", c.Name, c.Status)
		}
	}
}

func TestCheckServerWarnsWhenHealthzHasNoStatus(t *testing.T) {
	// A 200 /healthz with no status field (e.g. a proxy returning {}) must warn,
	// not silently pass and not crash.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for _, c := range checkServer(srv.URL) {
		if c.Name == "server-health" && c.Status != StatusWarn {
			t.Fatalf("missing-status /healthz must warn, got %q (%s)", c.Status, c.Detail)
		}
	}
}
