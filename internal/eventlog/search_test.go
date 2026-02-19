package eventlog

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func TestSearch_ByService(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("checkout", now.Add(-3*time.Second)),
		makeTestEvent("payment", now.Add(-2*time.Second)),
		makeTestEvent("checkout", now.Add(-1*time.Second)),
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
	var events []event.WideEvent
	for i := 0; i < 10; i++ {
		events = append(events, makeTestEvent("svc", now.Add(time.Duration(-i)*time.Second)))
	}
	writeTestEvents(t, dir, "events-20260101-000000.jsonl", events)

	got, err := Search(dir, SearchFilter{Service: "svc", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 events (limit), got %d", len(got))
	}
}

func TestSearch_SortedDesc(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Hour)
	t3 := t1.Add(2 * time.Hour)
	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("svc", t1),
		makeTestEvent("svc", t3),
		makeTestEvent("svc", t2),
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

	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("checkout", now.Add(-1*time.Second)),
		errEv,
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

	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("svc", t0),
		makeTestEvent("svc", t1),
		makeTestEvent("svc", t2),
		makeTestEvent("svc", t3),
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
	writeTestEvents(t, dir, "events-20260101-000000.jsonl", []event.WideEvent{
		makeTestEvent("svc", now),
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
