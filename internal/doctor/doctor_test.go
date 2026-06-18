package doctor

import "testing"

func TestRunLocalOnlyHasNoServerChecks(t *testing.T) {
	// Hermetic: clear the env doctor reads and point the WAL dir at a temp dir
	// so this test neither depends on the developer's env nor probes the cwd.
	for _, k := range EnvKeys {
		t.Setenv(k, "")
	}
	t.Setenv("EVENT_LOG_V2_DIR", t.TempDir())
	r := Run(Options{}) // ServerChecks false
	if len(r.Checks) == 0 {
		t.Fatal("expected local checks")
	}
	names := map[string]bool{}
	for _, c := range r.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"auth/config", "wal-dir", "sqlite", "triage-hash"} {
		if !names[want] {
			t.Fatalf("missing local check %q", want)
		}
	}
	if names["server-livez"] || names["server-readyz"] {
		t.Fatal("server checks must not run without ServerChecks")
	}
}

func TestRunLocalChecksDoNotFailWithoutServer(t *testing.T) {
	// Local checks should be ok/warn/skip — never fail merely because no server
	// is running. Clear every key processEnv reads so dev-machine exports (e.g.
	// auth keys) can't make ParseConfig error and turn this into a false failure.
	// Empty WAYLOG_PROFILE defaults to dev in ParseConfig.
	for _, k := range EnvKeys {
		t.Setenv(k, "")
	}
	// Point the WAL dir at a writable temp dir so the (never-skipped) wal-dir
	// check passes hermetically without probing the process's cwd.
	t.Setenv("EVENT_LOG_V2_DIR", t.TempDir())
	r := Run(Options{})
	if !r.OK() {
		t.Fatalf("local-only run must not fail: %+v", r.Checks)
	}
}
