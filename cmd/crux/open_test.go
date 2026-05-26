package main

import (
	"reflect"
	"testing"
)

func TestBrowserCommandFor(t *testing.T) {
	cases := []struct {
		goos     string
		wantCmd  string
		wantArgs []string
	}{
		{goos: "darwin", wantCmd: "open"},
		{goos: "linux", wantCmd: "xdg-open"},
		{goos: "windows", wantCmd: "cmd", wantArgs: []string{"/c", "start"}},
		{goos: "plan9"},
	}
	for _, c := range cases {
		gotCmd, gotArgs := browserCommandFor(c.goos)
		if gotCmd != c.wantCmd || !reflect.DeepEqual(gotArgs, c.wantArgs) {
			t.Fatalf("browserCommandFor(%q) = (%q, %#v), want (%q, %#v)", c.goos, gotCmd, gotArgs, c.wantCmd, c.wantArgs)
		}
	}
}
