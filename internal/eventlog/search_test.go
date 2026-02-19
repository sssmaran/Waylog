package eventlog

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func TestSearch_ByService(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("checkout", now.Add(-3*time.Second), now.Add(-3*time.Second), true),
		makeTestEntry("payment", now.Add(-2*time.Second), now.Add(-2*time.Second), true),
		makeTestEntry("checkout", now.Add(-1*time.Second), now.Add(-1*time.Second), true),
	})

	got, err := Search(dir, SearchFilter{Service: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 checkout events, got %d", len(got))
	}
	for _, ev := range got {
		if ev.System.Service != "checkout" {
			t.Errorf("expected service=checkout, got %q", ev.System.Service)
		}
	}
}

func TestSearch_Limit(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	var entries []LogEntry
	for i := 0; i < 10; i++ {
		ts := now.Add(time.Duration(-i) * time.Second)
		entries = append(entries, makeTestEntry("svc", ts, ts, true))
	}
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", entries)

	got, err := Search(dir, SearchFilter{Service: "svc", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 events (limit), got %d", len(got))
	}
}

func TestSearch_ReturnsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Hour)
	t3 := t1.Add(2 * time.Hour)

	// Spread across two files — oldest file first
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("svc", t1, t1, true),
	})
	writeTestEntries(t, dir, "events-20260101-010000.jsonl", []LogEntry{
		makeTestEntry("svc", t2, t2, true),
		makeTestEntry("svc", t3, t3, true),
	})

	got, err := Search(dir, SearchFilter{Service: "svc", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	// Should get t3, t2 (newest), NOT t1, t2 (oldest)
	if !got[0].Timestamp.Equal(t3) {
		t.Errorf("first result timestamp = %v, want %v (newest)", got[0].Timestamp, t3)
	}
	if !got[1].Timestamp.Equal(t2) {
		t.Errorf("second result timestamp = %v, want %v", got[1].Timestamp, t2)
	}
}

func TestSearch_SortedDesc(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Hour)
	t3 := t1.Add(2 * time.Hour)
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("svc", t1, t1, true),
		makeTestEntry("svc", t3, t3, true),
		makeTestEntry("svc", t2, t2, true),
	})

	got, err := Search(dir, SearchFilter{Service: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.After(got[i-1].Timestamp) {
			t.Errorf("results not sorted desc at index %d", i)
		}
	}
}

func TestSearch_ByErrorCode(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	errEv := makeTestEvent("payment", now)
	errEv.Outcome.Success = false
	errEv.Error = &event.ErrorContext{Code: "PMT_502", Message: "failed"}
	errEv.EventName = "payment.error"

	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("checkout", now.Add(-1*time.Second), now.Add(-1*time.Second), true),
		{LoggedAt: now, Event: errEv, SampledInGraph: true},
	})

	got, err := Search(dir, SearchFilter{ErrorCode: "PMT_502"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(got))
	}
	if got[0].Error.Code != "PMT_502" {
		t.Errorf("error code = %q, want PMT_502", got[0].Error.Code)
	}
}

func TestSearch_TimeWindow(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)
	t3 := t0.Add(3 * time.Hour)

	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("svc", t0, t0, true),
		makeTestEntry("svc", t1, t1, true),
		makeTestEntry("svc", t2, t2, true),
		makeTestEntry("svc", t3, t3, true),
	})

	got, err := Search(dir, SearchFilter{
		Service: "svc",
		Start:   t1,
		End:     t2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events in window, got %d", len(got))
	}
}

func TestSearch_MaxLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeTestEntries(t, dir, "events-20260101-000000.jsonl", []LogEntry{
		makeTestEntry("svc", now, now, true),
	})

	got, err := Search(dir, SearchFilter{Service: "svc", Limit: 999})
	if err != nil {
		t.Fatal(err)
	}
	// Should not error; limit capped to 200
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
}
