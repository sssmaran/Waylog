package transporthttp

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func validEvent(id string, status eventv2.Status) *eventv2.Event {
	return &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       id,
		TsStart:       time.Unix(0, 0),
		TsEnd:         time.Unix(1, 0),
		DurationMS:    1,
		Kind:          "http",
		Service:       "checkout",
		Env:           "test",
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "fedcba9876543210",
		ParentSpanID:  "",
		Status:        status,
	}
}

func TestClientSinglePost(t *testing.T) {
	var got eventv2.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			t.Fatalf("path=%s want /v1/events", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type=%s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Fatalf("authorization=%q want bearer", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":1,"duplicate":0,"rejected":[]}`)
	}))
	defer srv.Close()

	cli, err := New(Config{IngestURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cli.Submit(validEvent("e1", eventv2.StatusOK))
	cli.Shutdown(2 * time.Second)
	if got.EventID != "e1" {
		t.Fatalf("server got %+v", got)
	}
}

func TestSinglePostCountsEnvelopeRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":0,"duplicate":0,"rejected":[{"index":0,"event_id":"e1","reason":"validation_failed"}]}`)
	}))
	defer srv.Close()

	cli, err := New(Config{IngestURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cli.Submit(validEvent("e1", eventv2.StatusError)) {
		t.Fatal("single submit should report false when envelope rejects the event")
	}
	if got := cli.Rejected(); got != 1 {
		t.Fatalf("Rejected=%d want 1", got)
	}
}

func TestNDJSONBatchFlush(t *testing.T) {
	var batches atomic.Int64
	var totalEvents atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("content-type=%s", r.Header.Get("Content-Type"))
		}
		sc := bufio.NewScanner(r.Body)
		n := int64(0)
		for sc.Scan() {
			var e eventv2.Event
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Fatalf("decode: %v", err)
			}
			n++
		}
		totalEvents.Add(n)
		batches.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":1,"duplicate":0,"rejected":[]}`)
	}))
	defer srv.Close()

	cli, err := New(Config{IngestURL: srv.URL, BatchMode: true, BatchAgeMs: 20})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 300; i++ {
		cli.Submit(validEvent(itoa(i), eventv2.StatusOK))
	}
	cli.Shutdown(3 * time.Second)

	if totalEvents.Load() != 300 {
		t.Fatalf("total events=%d want 300", totalEvents.Load())
	}
	if batches.Load() < 2 {
		t.Fatalf("expected at least 2 batches, got %d", batches.Load())
	}
}

func TestNDJSONBatchCountsEnvelopeRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"accepted":0,"duplicate":0,"rejected":[{"index":0,"event_id":"e1","reason":"validation_failed"}]}`)
	}))
	defer srv.Close()

	cli, err := New(Config{IngestURL: srv.URL, BatchMode: true, BatchAgeMs: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cli.Submit(validEvent("e1", eventv2.StatusError))
	cli.Shutdown(2 * time.Second)
	if got := cli.Rejected(); got != 1 {
		t.Fatalf("Rejected=%d want 1", got)
	}
}

func TestQueueEvictsOKBeforePriority(t *testing.T) {
	var flushed []string
	q := newQueue(Config{
		MaxBatch:     256,
		MaxBatchSize: 1 << 20,
		BatchAgeMs:   50,
		OkBudgetPct:  70,
		InFlightCap:  600,
	}, func(batch []*eventv2.Event) deliveryResult {
		for _, ev := range batch {
			flushed = append(flushed, ev.EventID)
		}
		return deliveryResult{success: true}
	}, nil)
	go q.run()

	for i := 0; i < 6; i++ {
		q.enqueue(validEvent("ok-"+itoa(i), eventv2.StatusOK))
	}
	q.enqueue(validEvent("prio-1", eventv2.StatusError))
	q.shutdown(2 * time.Second)

	if len(flushed) == 0 {
		t.Fatal("expected flushed events")
	}
	foundPriority := false
	for _, id := range flushed {
		if id == "prio-1" {
			foundPriority = true
			break
		}
	}
	if !foundPriority {
		t.Fatalf("priority event was evicted before ok events: %+v", flushed)
	}
}

func TestQueuePressureEvictionsIncrementDrops(t *testing.T) {
	var drops atomic.Int64
	q := newQueue(Config{
		MaxBatch:     256,
		MaxBatchSize: 1 << 20,
		BatchAgeMs:   50,
		OkBudgetPct:  70,
		InFlightCap:  600,
	}, func(batch []*eventv2.Event) deliveryResult {
		return deliveryResult{success: true}
	}, func(n int) {
		drops.Add(int64(n))
	})

	for i := 0; i < 4; i++ {
		q.enqueue(validEvent("ok-"+itoa(i), eventv2.StatusOK))
	}

	if drops.Load() == 0 {
		t.Fatal("expected queue pressure evictions to increment drops")
	}
}

func TestNewRejectsInvalidIngestURL(t *testing.T) {
	if _, err := New(Config{IngestURL: "localhost:8080"}); err == nil {
		t.Fatal("expected invalid ingest URL error")
	}
}

func TestBatchRetriesTransientFailure(t *testing.T) {
	var calls atomic.Int64
	var totalEvents atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		sc := bufio.NewScanner(r.Body)
		for sc.Scan() {
			totalEvents.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli, err := New(Config{
		IngestURL:  srv.URL,
		BatchMode:  true,
		BatchAgeMs: 1,
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cli.Submit(validEvent("retry-me", eventv2.StatusError))
	deadline := time.After(2 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected retry after transient failure, got %d calls", calls.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cli.Shutdown(2 * time.Second)

	if totalEvents.Load() != 1 {
		t.Fatalf("events delivered=%d want 1", totalEvents.Load())
	}
	if cli.Failures() == 0 {
		t.Fatal("transient failure should increment failure counter")
	}
}

func TestBatchDropsPermanentFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	cli, err := New(Config{IngestURL: srv.URL, BatchMode: true, BatchAgeMs: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cli.Submit(validEvent("bad", eventv2.StatusError))
	cli.Shutdown(2 * time.Second)

	if cli.Dropped() != 1 {
		t.Fatalf("Dropped=%d want 1", cli.Dropped())
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := [20]byte{}
	n := len(digits)
	for i > 0 {
		n--
		digits[n] = byte('0' + i%10)
		i /= 10
	}
	return string(digits[n:])
}
