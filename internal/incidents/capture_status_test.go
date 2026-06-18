package incidents

import (
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

// Invariant (e): capture status must never misreport completeness.
// A faulted/empty capture must be missing or partial, never OK.
func TestPropagationCaptureStatusHonesty(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-time.Minute)

	// nil story (reader faulted) => must be missing, never OK.
	missing := newPropagationEvidence(nil, "trace-x", nil, now)
	if missing.CaptureStatus != CaptureMissing {
		t.Fatalf("nil story must be CaptureMissing, got %q", missing.CaptureStatus)
	}

	// complete story (anchor + path + firstSeen) => OK.
	full := newPropagationEvidence(&apiv2.StoryResponse{
		Service: "checkout",
		Anchor:  &apiv2.StoryAnchor{Step: "charge"},
		Path:    []apiv2.StoryStep{{Name: "charge"}},
	}, "trace-x", &firstSeen, now)
	if full.CaptureStatus != CaptureOK {
		t.Fatalf("complete story must be CaptureOK, got %q", full.CaptureStatus)
	}

	// missing anchor => partial, never OK.
	partial := newPropagationEvidence(&apiv2.StoryResponse{
		Service: "checkout",
		Path:    []apiv2.StoryStep{{Name: "charge"}},
	}, "trace-x", &firstSeen, now)
	if partial.CaptureStatus != CapturePartial {
		t.Fatalf("missing anchor must be CapturePartial, got %q", partial.CaptureStatus)
	}
}
