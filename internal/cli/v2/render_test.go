package cliv2

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
