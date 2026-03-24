package coldstore

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func seedEvents(t *testing.T, db *SQLiteStore, n int, service string, errCode string) {
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

// seedEventsDirect inserts n events via direct SQL for fine-grained control.
func seedEventsDirect(t *testing.T, s *SQLiteStore, n int, customize func(i int) (service, traceID, errCode, errMsg string, success int, ts time.Time)) {
	t.Helper()
	for i := 0; i < n; i++ {
		svc, traceID, errCode, errMsg, success, ts := customize(i)
		_, err := s.writer.Exec(`
			INSERT INTO events (trace_id, span_id, event_name, service, env, user_id,
				status_code, success, error_code, error_message, latency_ms, timestamp)
			VALUES (?, ?, ?, ?, 'prod', ?, ?, ?, ?, ?, ?, ?)`,
			traceID,
			fmt.Sprintf("span-%03d", i),
			svc+".request",
			svc,
			fmt.Sprintf("user-%03d", i),
			200,
			success,
			errCode,
			errMsg,
			int64(10+i),
			ts.UTC().Format(tsFormat),
		)
		if err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}
}

func TestSearchByService(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	seedEvents(t, db, 5, "checkout", "")
	seedEvents(t, db, 3, "payment", "")

	page, err := managed.SearchEvents(SearchFilter{Service: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 5 {
		t.Fatalf("got %d results, want 5", len(page.Results))
	}
	for _, r := range page.Results {
		if r.Service != "checkout" {
			t.Errorf("got service %q, want checkout", r.Service)
		}
	}
}

func TestSearchByTraceID(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	seedEventsDirect(t, s, 50, func(i int) (string, string, string, string, int, time.Time) {
		return "svc-a", fmt.Sprintf("trace-%03d", i), "", "", 1, base.Add(time.Duration(i) * time.Second)
	})

	page, err := s.SearchEvents(SearchFilter{TraceID: "trace-025"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(page.Results))
	}
	if page.Results[0].TraceID != "trace-025" {
		t.Errorf("trace_id: got %q, want %q", page.Results[0].TraceID, "trace-025")
	}
	if page.TotalCount != 1 {
		t.Errorf("total_count: got %d, want 1", page.TotalCount)
	}
}

func TestSearchByServiceAndErrorCode(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	seedEventsDirect(t, s, 50, func(i int) (string, string, string, string, int, time.Time) {
		svc := "svc-a"
		if i%3 == 0 {
			svc = "svc-b"
		}
		errCode := ""
		errMsg := ""
		success := 1
		if i%7 == 0 {
			errCode = fmt.Sprintf("ERR_%d", i%3)
			errMsg = "something failed"
			success = 0
		}
		return svc, fmt.Sprintf("trace-%03d", i), errCode, errMsg, success, base.Add(time.Duration(i) * time.Second)
	})

	page, err := s.SearchEvents(SearchFilter{Service: "svc-b", ErrorCode: "ERR_0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) == 0 {
		t.Fatal("expected results, got 0")
	}
	for _, r := range page.Results {
		if r.Service != "svc-b" {
			t.Errorf("service: got %q, want svc-b", r.Service)
		}
		if r.ErrorCode != "ERR_0" {
			t.Errorf("error_code: got %q, want ERR_0", r.ErrorCode)
		}
	}
}

func TestSearchCursorPagination(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	seedEventsDirect(t, s, 30, func(i int) (string, string, string, string, int, time.Time) {
		return "svc-a", fmt.Sprintf("trace-%03d", i), "", "", 1, base.Add(time.Duration(i) * time.Second)
	})

	seen := make(map[int64]bool)
	var cursor int64
	pages := 0

	for {
		page, err := s.SearchEvents(SearchFilter{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.TotalCount != 30 {
			t.Errorf("page %d: total_count: got %d, want 30", pages, page.TotalCount)
		}
		for _, r := range page.Results {
			if seen[r.ID] {
				t.Errorf("duplicate row id=%d on page %d", r.ID, pages)
			}
			seen[r.ID] = true
		}
		pages++
		if page.NextCursor == 0 {
			break
		}
		cursor = page.NextCursor
	}

	if pages != 3 {
		t.Errorf("expected 3 pages, got %d", pages)
	}
	if len(seen) != 30 {
		t.Errorf("expected 30 unique rows, got %d", len(seen))
	}
}

func TestSearchTimeWindow(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	seedEventsDirect(t, s, 100, func(i int) (string, string, string, string, int, time.Time) {
		return "svc-a", fmt.Sprintf("trace-%03d", i), "", "", 1, base.Add(time.Duration(i) * time.Second)
	})

	start := base.Add(20 * time.Second)
	end := base.Add(40 * time.Second)

	page, err := s.SearchEvents(SearchFilter{Start: start, End: end, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Results {
		if r.Timestamp.Before(start) || r.Timestamp.After(end) {
			t.Errorf("result timestamp %v outside window [%v, %v]", r.Timestamp, start, end)
		}
	}
	// 20..40 inclusive = 21 events
	if len(page.Results) != 21 {
		t.Errorf("expected 21 results, got %d", len(page.Results))
	}
	if page.TotalCount != 21 {
		t.Errorf("total_count: got %d, want 21", page.TotalCount)
	}
}

func TestSearchLimitClamped(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	seedEventsDirect(t, s, 60, func(i int) (string, string, string, string, int, time.Time) {
		return "svc-a", fmt.Sprintf("trace-%03d", i), "", "", 1, base.Add(time.Duration(i) * time.Second)
	})

	// limit=0 defaults to 50.
	page, err := s.SearchEvents(SearchFilter{Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 50 {
		t.Errorf("limit=0: got %d results, want 50", len(page.Results))
	}

	// limit=500 clamped to 200, but only 60 rows exist.
	page, err = s.SearchEvents(SearchFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 60 {
		t.Errorf("limit=500: got %d results, want 60 (all rows)", len(page.Results))
	}
}

func TestSearchByErrorCode(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	seedEvents(t, db, 2, "svc", "")        // success
	seedEvents(t, db, 3, "svc", "PMT_502") // errors

	page, err := managed.SearchEvents(SearchFilter{ErrorCode: "PMT_502"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(page.Results))
	}
	for _, r := range page.Results {
		if r.ErrorCode != "PMT_502" {
			t.Errorf("got error_code %q, want PMT_502", r.ErrorCode)
		}
		if r.Success {
			t.Error("expected success=false for error rows")
		}
	}
}

func TestSearchLimit(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	seedEvents(t, db, 20, "svc", "")

	page, err := managed.SearchEvents(SearchFilter{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 5 {
		t.Fatalf("got %d results, want 5", len(page.Results))
	}
}

func TestSearchPerformance100K(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100K performance test in short mode")
	}

	s := newTestStore(t)

	// Bulk insert 100K events using batched transactions
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, err := s.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO events (trace_id, span_id, event_name, service, env, user_id, status_code, success, latency_ms, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100_000; i++ {
		ts := base.Add(time.Duration(i) * time.Millisecond)
		traceID := fmt.Sprintf("t-%06d", i)
		svc := fmt.Sprintf("svc-%d", i%10)
		_, err := stmt.Exec(traceID, fmt.Sprintf("s-%06d", i), svc+".request", svc, "prod", "u-"+fmt.Sprintf("%d", i%100), 200, 1, 10+i%100, ts.UTC().Format(tsFormat))
		if err != nil {
			t.Fatal(err)
		}
		if i%10000 == 0 && i > 0 {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			tx, err = s.writer.Begin()
			if err != nil {
				t.Fatal(err)
			}
			stmt, err = tx.Prepare(
				`INSERT INTO events (trace_id, span_id, event_name, service, env, user_id, status_code, success, latency_ms, timestamp)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Run 100 searches by trace_id, measure P95
	durations := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		traceID := fmt.Sprintf("t-%06d", i*1000)
		start := time.Now()
		page, err := s.SearchEvents(SearchFilter{TraceID: traceID})
		durations[i] = time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Results) == 0 {
			t.Fatalf("no results for %s", traceID)
		}
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[94]
	t.Logf("100K search P95: %v", p95)
	if p95 > 100*time.Millisecond {
		t.Errorf("P95 = %v, want < 100ms", p95)
	}
}

func TestSearchNewestFirst(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	seedEvents(t, db, 10, "svc", "")

	page, err := managed.SearchEvents(SearchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) < 2 {
		t.Fatal("need at least 2 results")
	}
	// Ordered by id DESC, so IDs should be descending.
	for i := 0; i < len(page.Results)-1; i++ {
		if page.Results[i].ID < page.Results[i+1].ID {
			t.Errorf("results[%d].ID (%d) < results[%d].ID (%d) — not newest-first",
				i, page.Results[i].ID, i+1, page.Results[i+1].ID)
		}
	}
}
