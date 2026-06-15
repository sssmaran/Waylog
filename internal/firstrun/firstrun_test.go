package firstrun

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

func TestRunEndToEndOpensRealIncident(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required for source-checkout first-run")
	}
	var out bytes.Buffer
	err := Run(Options{
		Requests: 30,
		Timeout:  90 * time.Second,
		Stdout:   &out,
		Stderr:   &out,
		NoWait:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v\n--- output ---\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"report_hash", "PMT_502", "crux incidents"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("output missing %q\n%s", want, got)
		}
	}
}
