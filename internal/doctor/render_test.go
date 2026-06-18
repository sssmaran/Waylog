package doctor

import (
	"strings"
	"testing"
)

func TestResultOKIgnoresWarnAndSkip(t *testing.T) {
	r := Result{Checks: []Check{
		{Name: "a", Status: StatusOK},
		{Name: "b", Status: StatusWarn, Detail: "weak key"},
		{Name: "c", Status: StatusSkip, Detail: "SQLITE_PATH unset"},
	}}
	if !r.OK() {
		t.Fatal("warn/skip must not make Result not-OK")
	}
	r.Checks = append(r.Checks, Check{Name: "d", Status: StatusFail, Detail: "boom"})
	if r.OK() {
		t.Fatal("a failed check must make Result not-OK")
	}
}

func TestRenderShowsEveryCheckAndDetail(t *testing.T) {
	r := Result{Checks: []Check{
		{Name: "auth/config", Status: StatusOK},
		{Name: "sqlite", Status: StatusFail, Detail: "cannot open"},
	}}
	var b strings.Builder
	Render(&b, r)
	out := b.String()
	for _, want := range []string{"auth/config", "sqlite", "cannot open", "ok", "fail"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q in:\n%s", want, out)
		}
	}
}
