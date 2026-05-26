package main

import (
	"reflect"
	"testing"
)

func TestSplitCruxArgs_REPLWithGlobalFlags(t *testing.T) {
	global, rest, ingestURL := splitCruxArgs([]string{"--addr", ":8081", "--api-key", "demo", "--timeout", "30s"})
	if ingestURL != ":8081" {
		t.Fatalf("ingestURL = %q, want :8081", ingestURL)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %#v, want empty", rest)
	}
	wantGlobal := []string{"--addr", "http://localhost:8081", "--api-key", "demo", "--timeout", "30s"}
	if !reflect.DeepEqual(global, wantGlobal) {
		t.Fatalf("global = %#v, want %#v", global, wantGlobal)
	}
}

func TestSplitCruxArgs_PreservesTimeoutEqualsForm(t *testing.T) {
	global, rest, _ := splitCruxArgs([]string{"--timeout=30s"})
	if len(rest) != 0 {
		t.Fatalf("rest = %#v, want empty", rest)
	}
	found := false
	for i := 0; i < len(global)-1; i++ {
		if global[i] == "--timeout" && global[i+1] == "30s" {
			found = true
		}
	}
	if !found {
		t.Fatalf("global = %#v, missing --timeout 30s", global)
	}
}

func TestSplitCruxArgs_CommandModePreservesCommand(t *testing.T) {
	global, rest, _ := splitCruxArgs([]string{"--addr=http://localhost:8080", "incidents"})
	if len(global) < 2 || global[0] != "--addr" {
		t.Fatalf("global = %#v", global)
	}
	if !reflect.DeepEqual(rest, []string{"incidents"}) {
		t.Fatalf("rest = %#v, want incidents", rest)
	}
}
