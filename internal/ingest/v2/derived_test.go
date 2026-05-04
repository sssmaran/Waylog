package ingestv2

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestTraceStoryDirectSuppressedIsHeaderOnly(t *testing.T) {
	idx := NewRecentIndex(nil)
	ev := testTraceEvent("suppressed", "trace", "checkout", eventv2.StatusSuppressed, testTime(0))
	ev.Steps = []eventv2.Step{{Name: "hidden", StartMS: 0, DurationMS: 1, Status: eventv2.StepStatusOK}}
	idx.Insert(ev)

	story, ok := NewReader(idx).TraceStoryByEventID("suppressed")
	if !ok {
		t.Fatal("story not found")
	}
	if story.Linkage != LinkageDirect || story.Status != eventv2.StatusSuppressed {
		t.Fatalf("story=%+v", story)
	}
	if len(story.Path) != 0 || len(story.Logs) != 0 || len(story.Downstream) != 0 {
		t.Fatalf("suppressed story should be header-only: %+v", story)
	}
}

func TestTraceStoryByTraceExcludesSuppressedAndBuildsContributingWindow(t *testing.T) {
	idx := NewRecentIndex(nil)
	suppressedRoot := testTraceEvent("suppressed-root", "trace", "gateway", eventv2.StatusSuppressed, testTime(0))
	okChild := testTraceEvent("ok-child", "trace", "checkout", eventv2.StatusOK, testTime(1))
	okChild.ParentSpanID = "missing"
	okChild.Fields = map[string]any{"http": map[string]any{"route": "/buy"}}
	okChild.Anchor = &eventv2.Anchor{Step: "pay", ErrorCode: "PMT_502"}
	okChild.Steps = []eventv2.Step{
		{Name: "prepare", StartMS: 0, DurationMS: 5, Status: eventv2.StepStatusOK},
		{Name: "pay", StartMS: 10, DurationMS: 5, Status: eventv2.StepStatusError, Error: &eventv2.StepError{Code: "PMT_502", Reason: "gateway"}},
		{Name: "pay", StartMS: 20, DurationMS: 5, Status: eventv2.StepStatusError, Error: &eventv2.StepError{Code: "PMT_502", Reason: "final"}},
		{Name: "cleanup", StartMS: 30, DurationMS: 5, Status: eventv2.StepStatusOK},
	}
	okChild.Logs = []eventv2.Log{
		{TsOffsetMS: 4, Level: eventv2.LogLevelWarn, Msg: "early"},
		{TsOffsetMS: 24, Level: eventv2.LogLevelError, Msg: "anchor"},
		{TsOffsetMS: 31, Level: eventv2.LogLevelError, Msg: "late"},
	}
	idx.Insert(suppressedRoot)
	idx.Insert(okChild)

	story, ok := NewReader(idx).TraceStoryByTraceID("trace")
	if !ok {
		t.Fatal("story not found")
	}
	if story.TraceID != "trace" || story.Service != "checkout" || story.Route != "/buy" || story.Linkage != LinkageTimestampFallback {
		t.Fatalf("story=%+v", story)
	}
	if story.Anchor == nil || story.Anchor.ErrorCode != "PMT_502" {
		t.Fatalf("anchor=%+v", story.Anchor)
	}
	if len(story.Path) != 3 || story.Path[2].ErrorMsg != "final" {
		t.Fatalf("path=%+v", story.Path)
	}
	if len(story.Logs) != 2 || story.Logs[1].Msg != "anchor" {
		t.Fatalf("logs=%+v", story.Logs)
	}
}

func TestErrorsGroupsAndPaginates(t *testing.T) {
	idx := NewRecentIndex(nil)
	idx.Insert(errorEvent("a", "trace-a", "checkout", "charge", "PMT_502", testTime(3), "u1"))
	idx.Insert(errorEvent("b", "trace-b", "checkout", "charge", "PMT_502", testTime(2), "u2"))
	idx.Insert(errorEvent("c", "trace-c", "checkout", "reserve", "INV_409", testTime(1), ""))

	reader := NewReader(idx)
	filter := SearchFilter{Since: testTime(0), Until: testTime(10)}
	result := reader.Errors(filter, nil, 1)
	if len(result.Rows) != 1 || result.Rows[0].Count != 2 || result.Rows[0].AffectedTraces != 2 {
		t.Fatalf("result=%+v", result)
	}
	if result.Rows[0].AffectedUsers == nil || *result.Rows[0].AffectedUsers != 2 {
		t.Fatalf("affected_users=%v", result.Rows[0].AffectedUsers)
	}
	if got := stringsJoin(result.Rows[0].SampleTraces); got != "trace-a,trace-b" {
		t.Fatalf("sample_traces=%s", got)
	}
	if result.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	result = reader.Errors(filter, result.NextCursor, 10)
	if len(result.Rows) != 1 || result.Rows[0].ErrorFamily.ErrorCode != "INV_409" || result.Rows[0].AffectedUsers != nil {
		t.Fatalf("page2=%+v", result)
	}
}

func TestBlastRadiusKeyModesAndCounts(t *testing.T) {
	idx := NewRecentIndex(nil)
	idx.Insert(errorEvent("a", "trace-a", "checkout", "charge", "PMT_502", testTime(3), "u1"))
	idx.Insert(errorEvent("b", "trace-b", "payment", "charge", "PMT_502", testTime(2), "u2"))
	idx.Insert(testTraceEvent("ok", "trace-a", "cart", eventv2.StatusOK, testTime(4)))

	reader := NewReader(idx)
	filter := SearchFilter{Since: testTime(0), Until: testTime(10)}
	single := reader.BlastRadius(filter, BlastKeyMode{Key: BlastKey{Service: "checkout", Step: "charge", ErrorCode: "PMT_502"}})
	if single.ViewMode != apiv2.BlastViewSingleFamily || single.AffectedRequests != 1 || single.AffectedServices != 2 {
		t.Fatalf("single=%+v", single)
	}
	if single.AffectedUsers == nil || *single.AffectedUsers != 1 {
		t.Fatalf("single users=%v", single.AffectedUsers)
	}
	if got := stringsJoin(single.TopServices); got != "cart,checkout" {
		t.Fatalf("top_services=%s", got)
	}

	cross := reader.BlastRadius(filter, BlastKeyMode{Key: BlastKey{ErrorCode: "PMT_502"}, CrossCode: true})
	if cross.ViewMode != apiv2.BlastViewCrossFamily || cross.AffectedRequests != 2 || cross.AffectedServices != 3 {
		t.Fatalf("cross=%+v", cross)
	}
}

func TestReadHandlerDerivedEndpoints(t *testing.T) {
	h := newTestReadHandler(t, nil)
	h.reader.index.Insert(errorEvent("a", "trace-a", "checkout", "charge", "PMT_502", testTime(3), "u1"))

	rec := readGet(t, h.TraceStory, "/v1/traces/story")
	expectReadError(t, rec, http.StatusBadRequest, errorCodeBadRequest)

	rec = readGet(t, h.TraceStory, "/v1/traces/story?trace_id=trace-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var story StoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &story); err != nil {
		t.Fatal(err)
	}
	if story.TraceID != "trace-a" || story.Linkage != LinkageTimestampFallback {
		t.Fatalf("story=%+v", story)
	}

	rec = readGet(t, h.Errors, "/v1/errors?status=ok")
	expectReadError(t, rec, http.StatusBadRequest, errorCodeBadRequest)

	since := testTime(0).Format(time.RFC3339Nano)
	rec = readGet(t, h.Errors, "/v1/errors?since="+since)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var errorsBody errorsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errorsBody); err != nil {
		t.Fatal(err)
	}
	if len(errorsBody.Rows) != 1 || errorsBody.NextCursor != nil {
		t.Fatalf("errors=%+v", errorsBody)
	}

	rec = readGet(t, h.BlastRadius, `/v1/blast_radius?since=`+since+`&error_family=checkout:charge:PMT_502`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var blast BlastRadiusResult
	if err := json.Unmarshal(rec.Body.Bytes(), &blast); err != nil {
		t.Fatal(err)
	}
	if blast.ViewMode != apiv2.BlastViewSingleFamily || blast.Key.Service != "checkout" || blast.AffectedRequests != 1 {
		t.Fatalf("blast=%+v", blast)
	}

	rec = readGet(t, h.BlastRadius, `/v1/blast_radius?since=`+since+`&service=checkout&error_code=PMT_502`)
	expectReadError(t, rec, http.StatusBadRequest, errorCodeBadRequest)
}

func TestErrorFamilyDisplayParser(t *testing.T) {
	key, ok := ParseErrorFamily(`svc\:a:step\:b:CODE`)
	if !ok || key.Service != "svc:a" || key.Step != "step:b" || key.ErrorCode != "CODE" {
		t.Fatalf("key=%+v ok=%v", key, ok)
	}
	for _, raw := range []string{"a:b", `a:b:c\`, `a:b:c:d`, `a:\x:c`} {
		if _, ok := ParseErrorFamily(raw); ok {
			t.Fatalf("expected malformed: %q", raw)
		}
	}
}

func errorEvent(id, traceID, service, step, code string, ts time.Time, userID string) *eventv2.Event {
	ev := testTraceEvent(id, traceID, service, eventv2.StatusError, ts)
	ev.Anchor = &eventv2.Anchor{Step: step, ErrorCode: code}
	ev.Steps = []eventv2.Step{{Name: step, StartMS: 0, DurationMS: 10, Status: eventv2.StepStatusError, Error: &eventv2.StepError{Code: code, Reason: "failed"}}}
	if userID != "" {
		ev.Fields = map[string]any{"user": map[string]any{"id": userID}}
	}
	return ev
}
