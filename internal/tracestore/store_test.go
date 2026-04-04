package tracestore

import (
	"testing"
	"time"
)

func TestUpsert_NewTrace(t *testing.T) {
	s := NewStore()
	s.Upsert("trace-1", "req-1", &SpanRecord{
		SpanID:  "span-a",
		Service: "api-gateway",
		Success: true,
	})
	rec, ok := s.Get("trace-1")
	if !ok {
		t.Fatal("expected trace to exist")
	}
	if rec.TraceID != "trace-1" {
		t.Errorf("got TraceID=%q, want trace-1", rec.TraceID)
	}
	if rec.RequestID != "req-1" {
		t.Errorf("got RequestID=%q, want req-1", rec.RequestID)
	}
	if len(rec.Spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(rec.Spans))
	}
	if rec.Spans[0].SpanID != "span-a" {
		t.Errorf("got SpanID=%q, want span-a", rec.Spans[0].SpanID)
	}
}

func TestUpsert_AppendNewSpan(t *testing.T) {
	s := NewStore()
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-a", Service: "gw"})
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-b", Service: "checkout"})
	rec, _ := s.Get("trace-1")
	if len(rec.Spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(rec.Spans))
	}
}

func TestUpsert_EnrichStub(t *testing.T) {
	s := NewStore()
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-a"})
	s.Upsert("trace-1", "req-1", &SpanRecord{
		SpanID:    "span-a",
		Service:   "checkout",
		LatencyMs: 42,
		Success:   true,
	})
	rec, _ := s.Get("trace-1")
	if len(rec.Spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(rec.Spans))
	}
	if rec.Spans[0].Service != "checkout" {
		t.Errorf("stub not enriched: Service=%q", rec.Spans[0].Service)
	}
	if rec.Spans[0].LatencyMs != 42 {
		t.Errorf("stub not enriched: LatencyMs=%d", rec.Spans[0].LatencyMs)
	}
}

func TestUpsert_FirstNonZeroWins(t *testing.T) {
	s := NewStore()
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-a", Service: "first"})
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-a", Service: "second"})
	rec, _ := s.Get("trace-1")
	if rec.Spans[0].Service != "first" {
		t.Errorf("first non-zero should win, got Service=%q", rec.Spans[0].Service)
	}
}

func TestGet_UnknownTrace(t *testing.T) {
	s := NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for unknown trace")
	}
}

func TestUpsert_UpdatesUpdatedAt(t *testing.T) {
	s := NewStore()
	s.Upsert("trace-1", "req-1", &SpanRecord{SpanID: "span-a"})
	rec, _ := s.Get("trace-1")
	if rec.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set after upsert")
	}
}

func TestPruneOlderThan(t *testing.T) {
	s := NewStore()

	old := time.Now().Add(-3 * time.Hour)
	s.mu.Lock()
	rec := &TraceRecord{TraceID: "old-trace", RequestID: "req-old", UpdatedAt: old}
	rec.Spans = []SpanRecord{{SpanID: "s1", Service: "svc", Timestamp: old}}
	s.traces["old-trace"] = rec
	bucket := old.Truncate(time.Minute)
	s.traceLastBucket["old-trace"] = bucket
	s.cohorts = append([]*cohort{{bucket: bucket, traceIDs: map[string]struct{}{"old-trace": {}}}}, s.cohorts...)
	s.mu.Unlock()

	s.Upsert("new-trace", "req-new", &SpanRecord{SpanID: "s2", Service: "svc"})

	cutoff := time.Now().Add(-1 * time.Hour)
	s.PruneOlderThan(cutoff)

	_, ok := s.Get("old-trace")
	if ok {
		t.Error("old trace should be pruned")
	}
	_, ok = s.Get("new-trace")
	if !ok {
		t.Error("new trace should still exist")
	}
}

func TestForEachSpan(t *testing.T) {
	s := NewStore()
	s.Upsert("t1", "r1", &SpanRecord{SpanID: "a", Service: "gw"})
	s.Upsert("t1", "r1", &SpanRecord{SpanID: "b", Service: "checkout"})
	s.Upsert("t2", "r2", &SpanRecord{SpanID: "c", Service: "payment"})

	var count int
	start := time.Now().Add(-1 * time.Minute)
	end := time.Now().Add(1 * time.Minute)
	s.ForEachSpan(start, end, func(traceID string, span SpanRecord) {
		if traceID == "" {
			t.Fatal("traceID should be populated")
		}
		count++
	})
	if count != 3 {
		t.Errorf("got %d spans, want 3", count)
	}
}

func TestForEachSpan_TimeFiltered(t *testing.T) {
	s := NewStore()
	s.Upsert("t1", "r1", &SpanRecord{SpanID: "a", Service: "gw"})

	var count int
	start := time.Now().Add(-2 * time.Hour)
	end := time.Now().Add(-1 * time.Hour)
	s.ForEachSpan(start, end, func(traceID string, span SpanRecord) {
		count++
	})
	if count != 0 {
		t.Errorf("got %d spans, want 0 (outside window)", count)
	}
}
