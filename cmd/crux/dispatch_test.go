package main

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"incidents", []string{"incidents"}},
		{"triage inc_abc", []string{"triage", "inc_abc"}},
		{"blast checkout:payment.charge:PMT_502", []string{"blast", "checkout:payment.charge:PMT_502"}},
		{`explain "trace 1"`, []string{"explain", "trace 1"}},
		{"  triage    inc_abc   ", []string{"triage", "inc_abc"}},
	}
	for _, c := range cases {
		if got := parseLine(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("parseLine(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestDispatchBuiltins(t *testing.T) {
	var out, errOut bytes.Buffer
	d := newTestDispatcher()
	if res := d.Dispatch("", &out, &errOut); res.Kind != ResultNoop {
		t.Fatalf("empty result = %+v, want noop", res)
	}
	if res := d.Dispatch("help", &out, &errOut); res.Kind != ResultOK {
		t.Fatalf("help result = %+v, want ok", res)
	}
	if !strings.Contains(out.String(), "Commands:") || !strings.Contains(out.String(), "incidents") {
		t.Fatalf("help output = %q", out.String())
	}
	if res := d.Dispatch("exit", &out, &errOut); res.Kind != ResultExit {
		t.Fatalf("exit result = %+v, want exit", res)
	}
	if res := d.Dispatch("quit", &out, &errOut); res.Kind != ResultExit {
		t.Fatalf("quit result = %+v, want exit", res)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	d := newTestDispatcher()
	res := d.Dispatch("frobnicate", &out, &errOut)
	if res.Kind != ResultUnknown {
		t.Fatalf("result = %+v, want unknown", res)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestDispatchCLIWrappedPassesArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	var captured []string
	d := newTestDispatcher()
	d.globalArgs = []string{"--addr", "http://example.test"}
	d.runCLI = func(args []string, _ io.Reader, _, _ io.Writer) int {
		captured = append([]string(nil), args...)
		return 0
	}
	res := d.Dispatch("triage inc_abc", &out, &errOut)
	if res.Kind != ResultOK {
		t.Fatalf("result = %+v, want ok", res)
	}
	want := []string{"--addr", "http://example.test", "triage", "inc_abc"}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("args = %#v, want %#v", captured, want)
	}
}

func TestDispatchStatusAliasesCapabilities(t *testing.T) {
	var out, errOut bytes.Buffer
	var captured []string
	d := newTestDispatcher()
	d.runCLI = func(args []string, _ io.Reader, _, _ io.Writer) int {
		captured = append([]string(nil), args...)
		return 0
	}
	res := d.Dispatch("status", &out, &errOut)
	if res.Kind != ResultOK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if len(captured) == 0 || captured[len(captured)-1] != "capabilities" {
		t.Fatalf("status args = %#v, want capabilities", captured)
	}
}

func TestDispatchOpen(t *testing.T) {
	var out, errOut bytes.Buffer
	var opened string
	d := newTestDispatcher()
	d.openBrowser = func(target string) error {
		opened = target
		return nil
	}
	res := d.Dispatch("open inc_abc", &out, &errOut)
	if res.Kind != ResultOK {
		t.Fatalf("result = %+v, want ok", res)
	}
	if opened != "http://localhost:8080/ui/#/incident/inc_abc" {
		t.Fatalf("opened = %q", opened)
	}
}

func TestDispatchOpenRequiresID(t *testing.T) {
	var out, errOut bytes.Buffer
	d := newTestDispatcher()
	res := d.Dispatch("open", &out, &errOut)
	if res.Kind != ResultUsage {
		t.Fatalf("result = %+v, want usage", res)
	}
	if !strings.Contains(errOut.String(), "usage: open <incident_id>") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func newTestDispatcher() *Dispatcher {
	return &Dispatcher{
		ingestURL:   "http://localhost:8080",
		runCLI:      func(_ []string, _ io.Reader, _, _ io.Writer) int { return 0 },
		openBrowser: func(_ string) error { return nil },
	}
}
