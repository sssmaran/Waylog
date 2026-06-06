package cliv2

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/doctor"
)

// cleanDoctorEnv clears every env var doctor reads (doctor.EnvKeys is the single
// source of truth) so the doctor command's local checks pass deterministically
// regardless of the developer's exported environment.
func cleanDoctorEnv(t *testing.T) {
	t.Helper()
	for _, k := range doctor.EnvKeys {
		t.Setenv(k, "")
	}
	// Point the WAL dir at a writable temp dir so the wal-dir check passes
	// hermetically (it never skips) without probing the test's cwd.
	t.Setenv("EVENT_LOG_V2_DIR", t.TempDir())
}

func TestDoctorCommandRunsLocalChecks(t *testing.T) {
	cleanDoctorEnv(t)
	var out, errb bytes.Buffer
	code := RunCLI([]string{"doctor"}, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0; stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	for _, want := range []string{"auth/config", "triage-hash", "doctor: ok"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoctorCommandJSON(t *testing.T) {
	cleanDoctorEnv(t)
	var out, errb bytes.Buffer
	code := RunCLI([]string{"--json", "doctor"}, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("doctor --json exit = %d, want 0; stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"checks"`) {
		t.Fatalf("expected JSON with checks, got:\n%s", out.String())
	}
}

func TestDoctorRejectsUnknownFlag(t *testing.T) {
	cleanDoctorEnv(t)
	var out, errb bytes.Buffer
	code := RunCLI([]string{"doctor", "--bogus"}, nil, &out, &errb)
	if code == 0 {
		t.Fatalf("unknown doctor flag should be non-zero exit")
	}
}
