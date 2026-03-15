package coldstore

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func seedEvents(t *testing.T, db *Store, n int, service string, errCode string) {
	t.Helper()
	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     n + 10,
		BatchSize:     n + 10,
		FlushInterval: 50 * time.Millisecond,
	}, nil)
	bw.Start()
	for i := 0; i < n; i++ {
		success := errCode == ""
		ev := makeTestEvent(service, "aaaa000011112222aaaa000011112222", success)
		if errCode != "" {
			ev.Error = &event.ErrorContext{Code: errCode, Message: "test"}
			ev.Outcome.StatusCode = 502
		}
		ev.Timestamp = time.Now().Add(-time.Duration(n-i) * time.Second)
		bw.Enqueue(ev)
	}
	bw.Stop()
}

func TestSearchByService(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seedEvents(t, db, 5, "checkout", "")
	seedEvents(t, db, 3, "payment", "")

	results, err := db.SearchEvents(SearchFilter{Service: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	for _, r := range results {
		if r.Service != "checkout" {
			t.Errorf("got service %q, want checkout", r.Service)
		}
	}
}

func TestSearchByTraceID(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seedEvents(t, db, 3, "svc", "")

	results, err := db.SearchEvents(SearchFilter{TraceID: "aaaa000011112222aaaa000011112222"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestSearchByErrorCode(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seedEvents(t, db, 2, "svc", "")        // success
	seedEvents(t, db, 3, "svc", "PMT_502") // errors

	results, err := db.SearchEvents(SearchFilter{ErrorCode: "PMT_502"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for _, r := range results {
		if r.ErrorCode != "PMT_502" {
			t.Errorf("got error_code %q, want PMT_502", r.ErrorCode)
		}
		if r.Success {
			t.Error("expected success=false for error rows")
		}
	}
}

func TestSearchLimit(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seedEvents(t, db, 20, "svc", "")

	results, err := db.SearchEvents(SearchFilter{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
}

func TestSearchNewestFirst(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	seedEvents(t, db, 10, "svc", "")

	results, err := db.SearchEvents(SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Fatal("need at least 2 results")
	}
	for i := 0; i < len(results)-1; i++ {
		if results[i].Timestamp.Before(results[i+1].Timestamp) {
			t.Errorf("results[%d].Timestamp (%v) < results[%d].Timestamp (%v)",
				i, results[i].Timestamp, i+1, results[i+1].Timestamp)
		}
	}
}

func TestSearchTimeWindow(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Seed 10 events spread 1 second apart, ending ~now.
	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     20,
		BatchSize:     20,
		FlushInterval: 50 * time.Millisecond,
	}, nil)
	bw.Start()
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		ev := makeTestEvent("svc", "aaaa000011112222aaaa000011112222", true)
		ev.Timestamp = now.Add(-time.Duration(10-i) * time.Second)
		bw.Enqueue(ev)
	}
	bw.Stop()

	// Search for events in the last 5 seconds — should get a subset.
	results, err := db.SearchEvents(SearchFilter{
		Start: now.Add(-5 * time.Second),
		End:   now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 1 || len(results) > 6 {
		t.Fatalf("expected 1-6 results in 5s window, got %d", len(results))
	}
	for _, r := range results {
		if r.Timestamp.Before(now.Add(-6 * time.Second)) {
			t.Errorf("result timestamp %v is outside window", r.Timestamp)
		}
	}
}
