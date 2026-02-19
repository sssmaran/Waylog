package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func writeTestEntries(t *testing.T, dir, filename string, entries []LogEntry) {
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
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
}

// writeTestEvents writes bare WideEvents (legacy format) for backward compat tests.
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

func makeTestEntry(service string, ts, loggedAt time.Time, sampled bool) LogEntry {
	return LogEntry{
		LoggedAt:       loggedAt,
		Event:          makeTestEvent(service, ts),
		SampledInGraph: sampled,
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	entries := []LogEntry{
		makeTestEntry("svc-a", now.Add(-2*time.Second), now.Add(-2*time.Second), true),
		makeTestEntry("svc-b", now.Add(-1*time.Second), now.Add(-1*time.Second), true),
	}
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", entries)

	got, err := ReadFile(filepath.Join(dir, "events-20260101-000000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Event.System.Service != "svc-a" {
		t.Errorf("first event service = %q, want svc-a", got[0].Event.System.Service)
	}
}

func TestReadFile_BackwardCompat(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Write bare WideEvents (old format, no LogEntry wrapper)
	writeTestEvents(t, dir, "legacy.jsonl", []event.WideEvent{
		makeTestEvent("legacy-svc", ts),
	})

	got, err := ReadFile(filepath.Join(dir, "legacy.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	// Legacy events should get LoggedAt = ev.Timestamp and SampledInGraph = true
	if !got[0].LoggedAt.Equal(ts) {
		t.Errorf("logged_at = %v, want %v", got[0].LoggedAt, ts)
	}
	if !got[0].SampledInGraph {
		t.Error("expected SampledInGraph=true for legacy events")
	}
	if got[0].Event.System.Service != "legacy-svc" {
		t.Errorf("service = %q, want legacy-svc", got[0].Event.System.Service)
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
		t.Fatalf("expected 1 entry (skip malformed), got %d", len(got))
	}
}

func TestReadDir_FiltersByLoggedAt(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("old", t0, t0, true),
	})
	writeTestEntries(t, dir, "events-20260101-010000.jsonl", []LogEntry{
		makeTestEntry("new1", t1, t1, true),
		makeTestEntry("new2", t2, t2, true),
	})

	// Read entries logged after t0 — should get t1 and t2 only
	got, err := ReadDir(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after t0, got %d", len(got))
	}

	// Read all entries (zero time)
	all, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total entries, got %d", len(all))
	}
}

func TestReadDir_BackdatedTimestamp(t *testing.T) {
	// Event has a timestamp BEFORE snapshot, but was logged AFTER snapshot.
	// It must be included in replay (filtered by logged_at, not ev.Timestamp).
	dir := t.TempDir()
	snapshotAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Event timestamp is 1 hour before snapshot (backdated/late-arriving)
	evTimestamp := snapshotAt.Add(-1 * time.Hour)
	// But it was logged 1 minute after the snapshot
	loggedAt := snapshotAt.Add(1 * time.Minute)

	writeTestEntries(t, dir, "events-20260101-120100.jsonl", []LogEntry{
		makeTestEntry("late-svc", evTimestamp, loggedAt, true),
	})

	got, err := ReadDir(dir, snapshotAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry (backdated but logged after snapshot), got %d", len(got))
	}
}

func TestReadDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadDir(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries from empty dir, got %d", len(got))
	}
}
