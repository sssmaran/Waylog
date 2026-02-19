package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func writeTestEvents(t *testing.T, dir, filename string, events []event.WideEvent) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
}

func makeTestEvent(service string, ts time.Time) event.WideEvent {
	return event.WideEvent{
		SchemaVersion: "1.0",
		EventName:     service + ".request",
		Timestamp:     ts,
		User:          event.UserContext{ID: "u1"},
		Request:       event.RequestContext{TraceID: "aaaa0000bbbb1111cccc2222dddd3333"},
		System:        event.SystemContext{Service: service, Env: "test"},
		Outcome:       event.OutcomeContext{Success: true, StatusCode: 200},
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	events := []event.WideEvent{
		makeTestEvent("svc-a", now.Add(-2*time.Second)),
		makeTestEvent("svc-b", now.Add(-1*time.Second)),
	}
	writeTestEvents(t, dir, "events-20260101-000000.jsonl", events)

	got, err := ReadFile(filepath.Join(dir, "events-20260101-000000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].System.Service != "svc-a" {
		t.Errorf("first event service = %q, want svc-a", got[0].System.Service)
	}
}

func TestReadFile_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	if err := os.WriteFile(path, []byte("{bad json}\n"+`{"schema_version":"1.0","event_name":"x.request","timestamp":"2026-01-01T00:00:00Z","user":{"id":"u1"},"request":{"trace_id":"aaaa0000bbbb1111cccc2222dddd3333"},"system":{"service":"x","env":"test"},"outcome":{"success":true,"status_code":200}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event (skip malformed), got %d", len(got))
	}
}

func TestReadDir_FiltersByTimestamp(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("old", t0),
	})
	writeTestEvents(t, dir, "events-20260101-010000.jsonl", []event.WideEvent{
		makeTestEvent("new1", t1),
		makeTestEvent("new2", t2),
	})

	// Read events after t0 — should get t1 and t2 only
	got, err := ReadDir(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events after t0, got %d", len(got))
	}

	// Read all events (zero time)
	all, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total events, got %d", len(all))
	}
}

func TestReadDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 events from empty dir, got %d", len(got))
	}
}
