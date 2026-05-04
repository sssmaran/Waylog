package ingestv2

import (
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestProjectorIndexesHappyPathEvent(t *testing.T) {
	idx := NewRecentIndex(nil)
	NewProjector(idx).Project(testEvent("event-1", eventv2.StatusOK))

	if _, ok := idx.GetByID("event-1"); !ok {
		t.Fatal("event not indexed by id")
	}
	if got := len(idx.TraceEvents("trace-1")); got != 1 {
		t.Fatalf("trace events=%d want 1", got)
	}
	sizes := idx.Sizes()
	if sizes.Events != 1 || sizes.Traces != 1 || sizes.Services != 1 || sizes.Errors != 0 || sizes.Calls != 0 {
		t.Fatalf("sizes=%+v", sizes)
	}
}

func TestProjectorIndexesFailureTriplets(t *testing.T) {
	for _, status := range []eventv2.Status{eventv2.StatusError, eventv2.StatusTimeout, eventv2.StatusPartial, eventv2.StatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			idx := NewRecentIndex(nil)
			ev := testEvent("event-1", status)
			ev.Anchor = &eventv2.Anchor{Step: "payment.charge", ErrorCode: "PMT_502"}
			NewProjector(idx).Project(ev)

			key := ErrorKey{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"}
			node := idx.errors[key]
			if node == nil || node.Count != 1 {
				t.Fatalf("error node=%+v", node)
			}
		})
	}
}

func TestProjectorSuppressedCreatesNoFailureOrCallEdges(t *testing.T) {
	idx := NewRecentIndex(nil)
	ev := testEvent("event-1", eventv2.StatusSuppressed)
	ev.Steps = []eventv2.Step{{
		Name:       "payment.charge",
		StartMS:    0,
		DurationMS: 5,
		Status:     "ok",
		Downstream: &eventv2.Downstream{Service: "payment", Endpoint: "/charge"},
	}}
	NewProjector(idx).Project(ev)

	sizes := idx.Sizes()
	if sizes.Events != 1 || sizes.Services != 1 || sizes.Errors != 0 || sizes.Calls != 0 {
		t.Fatalf("sizes=%+v", sizes)
	}
}

func TestProjectorCallsOnlyFromStructuredDownstream(t *testing.T) {
	idx := NewRecentIndex(nil)
	ev := testEvent("event-1", eventv2.StatusOK)
	ev.Steps = []eventv2.Step{
		{Name: "payment.charge", StartMS: 0, DurationMS: 5, Status: "ok"},
		{Name: "call payment", StartMS: 5, DurationMS: 5, Status: "ok", Downstream: &eventv2.Downstream{Service: "payment", Endpoint: "/charge"}},
		{Name: "call payment again", StartMS: 10, DurationMS: 5, Status: "ok", Downstream: &eventv2.Downstream{Service: "payment", Endpoint: "/charge"}},
	}
	NewProjector(idx).Project(ev)

	if len(idx.calls) != 1 {
		t.Fatalf("calls=%+v", idx.calls)
	}
	key := CallKey{From: "checkout", To: "payment", Endpoint: "/charge"}
	edge := idx.calls[key]
	if edge == nil || edge.Count != 2 {
		t.Fatalf("edge=%+v", edge)
	}
}

func TestRecentIndexPruneOlderThan(t *testing.T) {
	idx := NewRecentIndex(nil)
	old := testEvent("old", eventv2.StatusError)
	old.TsEnd = time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)
	old.Anchor = &eventv2.Anchor{Step: "payment.charge", ErrorCode: "PMT_502"}
	newer := testEvent("new", eventv2.StatusOK)
	newer.TsEnd = old.TsEnd.Add(2 * time.Hour)
	NewProjector(idx).Project(old)
	NewProjector(idx).Project(newer)

	res := idx.PruneOlderThan(old.TsEnd.Add(time.Hour))
	if res.Events != 1 {
		t.Fatalf("pruned=%d want 1", res.Events)
	}
	if _, ok := idx.GetByID("old"); ok {
		t.Fatal("old event should be pruned")
	}
	if _, ok := idx.GetByID("new"); !ok {
		t.Fatal("new event should remain")
	}
	sizes := idx.Sizes()
	if sizes.Events != 1 || sizes.Errors != 0 {
		t.Fatalf("sizes=%+v", sizes)
	}
}

func TestRecentIndexPrunePreservesTraceOrder(t *testing.T) {
	idx := NewRecentIndex(nil)
	base := time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		ev := testEvent(id, eventv2.StatusOK)
		ev.TraceID = "trace-order"
		ev.TsEnd = base.Add(time.Duration(i) * time.Minute)
		NewProjector(idx).Project(ev)
	}

	idx.PruneOlderThan(base.Add(30 * time.Second))
	events := idx.TraceEvents("trace-order")
	if len(events) != 2 || events[0].EventID != "b" || events[1].EventID != "c" {
		t.Fatalf("events=%v", eventIDs(events))
	}
}

func TestRecentIndexPruneRemovesOldReadsAndKeepsCursorOrder(t *testing.T) {
	idx := NewRecentIndex(nil)
	base := time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)
	oldErr := testEvent("old-error", eventv2.StatusError)
	oldErr.TraceID = "trace-old"
	oldErr.TsStart = base
	oldErr.TsEnd = base.Add(10 * time.Millisecond)
	oldErr.Anchor = &eventv2.Anchor{Step: "payment.charge", ErrorCode: "PMT_502"}
	freshA := testEvent("fresh-a", eventv2.StatusOK)
	freshA.TraceID = "trace-a"
	freshA.TsStart = base.Add(1 * time.Hour)
	freshA.TsEnd = freshA.TsStart.Add(10 * time.Millisecond)
	freshB := testEvent("fresh-b", eventv2.StatusError)
	freshB.TraceID = "trace-b"
	freshB.TsStart = base.Add(2 * time.Hour)
	freshB.TsEnd = freshB.TsStart.Add(10 * time.Millisecond)
	freshB.Anchor = &eventv2.Anchor{Step: "db.load_cart", ErrorCode: "CART_NOT_FOUND"}

	projector := NewProjector(idx)
	projector.Project(oldErr)
	projector.Project(freshA)
	projector.Project(freshB)

	pruned := idx.PruneOlderThan(base.Add(30 * time.Minute))
	if pruned.Events != 1 {
		t.Fatalf("pruned=%d want 1", pruned.Events)
	}
	reader := NewReader(idx)
	filter := SearchFilter{Since: base, Until: base.Add(3 * time.Hour)}
	if _, ok := reader.GetEvent("old-error"); ok {
		t.Fatal("old event should be gone")
	}
	if got := reader.Errors(filter, nil, 10); len(got.Rows) != 1 || got.Rows[0].ErrorFamily.ErrorCode != "CART_NOT_FOUND" {
		t.Fatalf("errors=%+v", got)
	}
	page1 := reader.SearchEvents(filter, nil, 1)
	if got := ids(page1.Events); got != "fresh-b" || page1.NextCursor == nil {
		t.Fatalf("page1=%s cursor=%+v", got, page1.NextCursor)
	}
	page2 := reader.SearchEvents(filter, page1.NextCursor, 1)
	if got := ids(page2.Events); got != "fresh-a" || page2.NextCursor != nil {
		t.Fatalf("page2=%s cursor=%+v", got, page2.NextCursor)
	}
}

func eventIDs(events []*eventv2.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.EventID)
	}
	return out
}

func testEvent(id string, status eventv2.Status) *eventv2.Event {
	return &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       id,
		TsStart:       time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC),
		TsEnd:         time.Date(2026, 4, 25, 14, 0, 0, 10*int(time.Millisecond), time.UTC),
		DurationMS:    10,
		Kind:          "http",
		Service:       "checkout",
		Env:           "test",
		TraceID:       "trace-1",
		SpanID:        "span-1",
		Status:        status,
		Steps:         []eventv2.Step{},
		Logs:          []eventv2.Log{},
		Fields:        map[string]any{},
		Errors:        []eventv2.ErrorRef{},
	}
}
