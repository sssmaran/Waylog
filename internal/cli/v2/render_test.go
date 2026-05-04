package cliv2

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestRenderStoryPinsObservableLanguage(t *testing.T) {
	var out bytes.Buffer
	RenderStory(&out, StoryResponse{
		TraceID: "trace",
		Service: "checkout",
		Status:  eventv2.StatusError,
		Anchor:  &StoryAnchor{Step: "payment.charge", ErrorCode: "PMT_502"},
		Path:    []StoryStep{{Name: "payment.charge", Status: eventv2.StepStatusError, DurationMS: 12, ErrorMsg: "gateway"}},
		Linkage: apiv2.LinkageTimestampFallback,
	})
	if !strings.Contains(out.String(), "first observable failing step") {
		t.Fatalf("output missing required language:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "payment.charge -> PMT_502") {
		t.Fatalf("output missing anchor:\n%s", out.String())
	}
}

func TestRenderBlastPinsViewMode(t *testing.T) {
	var out bytes.Buffer
	RenderBlast(&out, BlastRadiusResponse{ViewMode: apiv2.BlastViewCrossFamily, Key: BlastKey{ErrorCode: "PMT_502"}})
	if !strings.Contains(out.String(), "view_mode: cross_family") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestRenderRecentPrintsTraceSummaryAndCursor(t *testing.T) {
	next := "next"
	anchor := "payment.charge/PMT_502"
	var out bytes.Buffer
	RenderRecent(&out, RecentTracesResponse{
		Traces: []TraceSummary{{
			TraceID:       "trace-1234567890",
			Status:        eventv2.StatusError,
			Services:      []string{"api-gateway", "checkout", "payment"},
			DurationMS:    42,
			AnchorSummary: &anchor,
			TsStart:       time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		}},
		NextCursor: &next,
	})
	got := out.String()
	for _, want := range []string{"TRACE", "error", "api-gateway -> checkout -> payment", "payment.charge/PMT_502", "next_cursor: next"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderEventPrintsSummaryCounts(t *testing.T) {
	var out bytes.Buffer
	RenderEvent(&out, &Event{
		EventID:    "event",
		TraceID:    "trace",
		Service:    "checkout",
		Status:     eventv2.StatusError,
		DurationMS: 12,
		Anchor:     &eventv2.Anchor{Step: "payment.charge", ErrorCode: "PMT_502"},
		Fields:     map[string]any{"http": map[string]any{"route": "/checkout"}},
		Steps: []eventv2.Step{
			{Name: "db.load_cart", Status: eventv2.StepStatusOK},
			{Name: "payment.charge", Status: eventv2.StepStatusError, Downstream: &eventv2.Downstream{Service: "payment"}},
		},
		Logs: []eventv2.Log{{Level: eventv2.LogLevelError, Msg: "upstream gateway 5xx"}},
	})
	got := out.String()
	for _, want := range []string{"event_id: event", "trace_id: trace", "route: /checkout", "anchor: payment.charge -> PMT_502", "steps: 2", "logs: 1", "downstream: 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderCapabilitiesPrintsReadableFlags(t *testing.T) {
	var out bytes.Buffer
	resp := CapabilitiesResponse{}
	resp.OTLP.HTTPTraces = true
	RenderCapabilities(&out, resp)
	if !strings.Contains(out.String(), "v2_reads: disabled") || !strings.Contains(out.String(), "otlp_http_traces: enabled") {
		t.Fatalf("output=%s", out.String())
	}
}

func TestRenderJSONPrettyPrints(t *testing.T) {
	var out bytes.Buffer
	if err := renderJSON(&out, ErrorsResponse{Rows: []ErrorRow{}}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "\n  ") {
		t.Fatalf("json=%q", out.String())
	}
}

func TestRenderNextCursor(t *testing.T) {
	next := "abc"
	var out bytes.Buffer
	RenderSearch(&out, EventSearchResponse{NextCursor: &next})
	if !strings.Contains(out.String(), "next_cursor: abc") {
		t.Fatalf("output=%s", out.String())
	}
}
