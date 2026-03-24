package coldstore

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func makeTestEvent(service, traceID string, success bool) event.WideEvent {
	ev := event.WideEvent{
		SchemaVersion: "1.0",
		EventName:     service + ".request",
		Timestamp:     time.Now(),
		User:          event.UserContext{ID: "u1"},
		Request:       event.RequestContext{TraceID: traceID, SpanID: "0123456789abcdef"},
		System:        event.SystemContext{Service: service, Env: "test", Version: "1.0"},
		Outcome:       event.OutcomeContext{Success: success, StatusCode: 200},
		Metrics:       event.MetricsContext{LatencyMs: 50},
	}
	if !success {
		ev.Outcome.StatusCode = 502
		ev.Error = &event.ErrorContext{Code: "ERR_502", Message: "bad gateway"}
	}
	return ev
}

func TestBatchWriter_WritesEvents(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     100,
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
	}, nil)
	bw.Start()
	defer bw.Stop()

	ev := makeTestEvent("checkout", "aaaa000011112222aaaa000011112222", true)
	if !bw.Enqueue(ev) {
		t.Fatal("enqueue should succeed")
	}

	time.Sleep(200 * time.Millisecond)

	var count int
	if err := db.reader.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 event in DB, got %d", count)
	}
}

func TestBatchWriter_BatchFlush(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     100,
		BatchSize:     5,
		FlushInterval: 10 * time.Second,
	}, nil)
	bw.Start()
	defer bw.Stop()

	for i := 0; i < 5; i++ {
		bw.Enqueue(makeTestEvent("svc", "aaaa000011112222aaaa000011112222", true))
	}

	time.Sleep(200 * time.Millisecond)

	var count int
	db.reader.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 events after batch flush, got %d", count)
	}
}

func TestBatchWriter_DropWhenFull(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     2,
		BatchSize:     100,
		FlushInterval: 10 * time.Second,
	}, nil)

	ev := makeTestEvent("svc", "aaaa000011112222aaaa000011112222", true)
	bw.Enqueue(ev)
	bw.Enqueue(ev)
	dropped := !bw.Enqueue(ev)
	if !dropped {
		t.Fatal("expected drop when queue full")
	}
}

func TestBatchWriter_StopDrains(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     100,
		BatchSize:     100,
		FlushInterval: 10 * time.Second,
	}, nil)
	bw.Start()

	for i := 0; i < 3; i++ {
		bw.Enqueue(makeTestEvent("svc", "aaaa000011112222aaaa000011112222", true))
	}

	bw.Stop()

	var count int
	db.reader.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if count != 3 {
		t.Fatalf("expected 3 events after stop-drain, got %d", count)
	}
}

func TestBatchWriter_ErrorEventFields(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	db := managed.(*SQLiteStore)

	bw := NewBatchWriter(db, BatchWriterConfig{
		QueueSize:     100,
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
	}, nil)
	bw.Start()
	defer bw.Stop()

	ev := makeTestEvent("payment", "bbbb000011112222bbbb000011112222", false)
	bw.Enqueue(ev)
	time.Sleep(200 * time.Millisecond)

	var errorCode, errorMsg string
	var success int
	err = db.reader.QueryRow("SELECT error_code, error_message, success FROM events WHERE trace_id = ?",
		"bbbb000011112222bbbb000011112222").Scan(&errorCode, &errorMsg, &success)
	if err != nil {
		t.Fatal(err)
	}
	if errorCode != "ERR_502" {
		t.Errorf("error_code = %q, want ERR_502", errorCode)
	}
	if success != 0 {
		t.Error("success should be 0 for failed event")
	}
}
