package incidents

import (
	"testing"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func ev(tsStart time.Time, step, code string) *eventv2.Event {
	return &eventv2.Event{TsStart: tsStart, Anchor: &eventv2.Anchor{Step: step, ErrorCode: code}}
}

func TestPickAnchorTsStart_StrictMatchEarliestWins(t *testing.T) {
	base := time.Unix(1700000000, 0)
	events := []*eventv2.Event{
		ev(base.Add(5*time.Second), "charge", "DB_TIMEOUT"),
		ev(base.Add(2*time.Second), "charge", "DB_TIMEOUT"),
		ev(base.Add(8*time.Second), "charge", "DB_TIMEOUT"),
	}
	got, ok := pickAnchorTsStart(events, apiv2.ErrorFamily{Step: "charge", ErrorCode: "DB_TIMEOUT"})
	if !ok {
		t.Fatal("ok=false")
	}
	if !got.Equal(base.Add(2 * time.Second)) {
		t.Errorf("got %v, want %v", got, base.Add(2*time.Second))
	}
}

func TestPickAnchorTsStart_FallsBackToStepOnly(t *testing.T) {
	base := time.Unix(1700000000, 0)
	events := []*eventv2.Event{
		ev(base.Add(5*time.Second), "charge", "OTHER_CODE"),
		ev(base.Add(3*time.Second), "charge", "ANOTHER"),
	}
	got, ok := pickAnchorTsStart(events, apiv2.ErrorFamily{Step: "charge", ErrorCode: "DB_TIMEOUT"})
	if !ok {
		t.Fatal("ok=false")
	}
	if !got.Equal(base.Add(3 * time.Second)) {
		t.Errorf("got %v, want %v", got, base.Add(3*time.Second))
	}
}

func TestPickAnchorTsStart_NoMatch(t *testing.T) {
	events := []*eventv2.Event{ev(time.Now(), "other_step", "X")}
	_, ok := pickAnchorTsStart(events, apiv2.ErrorFamily{Step: "charge", ErrorCode: "DB_TIMEOUT"})
	if ok {
		t.Fatal("expected ok=false on no step match")
	}
}

func TestUpdatePropagationSnapshot_FirstCaptureOK_OpeningEqualsLatest(t *testing.T) {
	now := time.Now()
	fresh := &PropagationEvidence{CapturedAt: now, CaptureStatus: CaptureOK, SampleTraceID: "t1"}
	snap := updatePropagationSnapshot(nil, fresh)
	if snap.Opening != fresh {
		t.Errorf("Opening != fresh")
	}
	if snap.Latest != fresh {
		t.Errorf("Latest != fresh")
	}
}

func TestUpdatePropagationSnapshot_FirstCaptureMissing_OpeningStaysNil_LatestRecorded(t *testing.T) {
	now := time.Now()
	fresh := &PropagationEvidence{CapturedAt: now, CaptureStatus: CaptureMissing}
	snap := updatePropagationSnapshot(nil, fresh)
	if snap.Opening != nil {
		t.Errorf("Opening should be nil on first missing capture")
	}
	if snap.Latest == nil || snap.Latest.CaptureStatus != CaptureMissing {
		t.Errorf("Latest should record missing capture; got %+v", snap.Latest)
	}
}

func TestUpdatePropagationSnapshot_PriorOpeningSet_CarriesForward(t *testing.T) {
	earlier := time.Now().Add(-time.Minute)
	prior := &PropagationSnapshot{
		Opening: &PropagationEvidence{CapturedAt: earlier, CaptureStatus: CaptureOK, SampleTraceID: "t_open"},
		Latest:  &PropagationEvidence{CapturedAt: earlier, CaptureStatus: CaptureOK, SampleTraceID: "t_open"},
	}
	fresh := &PropagationEvidence{CapturedAt: time.Now(), CaptureStatus: CaptureOK, SampleTraceID: "t_new"}
	snap := updatePropagationSnapshot(prior, fresh)
	if snap.Opening.SampleTraceID != "t_open" {
		t.Errorf("Opening should carry forward; got %q", snap.Opening.SampleTraceID)
	}
	if snap.Latest.SampleTraceID != "t_new" {
		t.Errorf("Latest should overwrite; got %q", snap.Latest.SampleTraceID)
	}
}

func TestUpdatePropagationSnapshot_PriorOpeningNil_NewOK_OpeningSet(t *testing.T) {
	prior := &PropagationSnapshot{
		Latest: &PropagationEvidence{CaptureStatus: CaptureMissing},
	}
	fresh := &PropagationEvidence{CapturedAt: time.Now(), CaptureStatus: CaptureOK, SampleTraceID: "t_ok"}
	snap := updatePropagationSnapshot(prior, fresh)
	if snap.Opening == nil || snap.Opening.SampleTraceID != "t_ok" {
		t.Errorf("Opening should be promoted on first OK capture; got %+v", snap.Opening)
	}
}

func TestUpdateBlastSnapshot_IndependentOfPropagation(t *testing.T) {
	now := time.Now()
	bfresh := &BlastEvidence{AffectedRequests: 5, CapturedAt: now, CaptureStatus: CaptureOK}
	snap := updateBlastSnapshot(nil, bfresh)
	if snap.Opening == nil || snap.Opening.AffectedRequests != 5 {
		t.Errorf("Blast.Opening should be set independently; got %+v", snap.Opening)
	}
}

func TestNewBlastEvidence_MissingStatusFromReaderFault(t *testing.T) {
	e := newBlastEvidence(apiv2.BlastRadiusResponse{}, time.Now(), CaptureMissing)
	if e.CaptureStatus != CaptureMissing {
		t.Errorf("CaptureStatus = %s; want missing", e.CaptureStatus)
	}
	if e.AffectedRequests != 0 || e.AffectedServices != 0 {
		t.Errorf("missing capture should carry zero counts; got %+v", e)
	}
}

func TestNewPropagationEvidence_NilStory_Missing(t *testing.T) {
	p := newPropagationEvidence(nil, "trace_x", nil, time.Now())
	if p.CaptureStatus != CaptureMissing {
		t.Errorf("CaptureStatus = %s; want missing", p.CaptureStatus)
	}
	if p.SampleTraceID != "trace_x" {
		t.Errorf("SampleTraceID lost; got %q", p.SampleTraceID)
	}
}

func TestNewPropagationEvidence_StoryWithoutAnchor_Partial(t *testing.T) {
	story := &apiv2.StoryResponse{Service: "payment-service", Path: []apiv2.StoryStep{{Name: "charge", Status: "error"}}}
	ts := time.Now()
	p := newPropagationEvidence(story, "tx", &ts, time.Now())
	if p.CaptureStatus != CapturePartial {
		t.Errorf("CaptureStatus = %s; want partial (no anchor)", p.CaptureStatus)
	}
}

func TestNewPropagationEvidence_StoryOK_FirstSeenNil_Partial(t *testing.T) {
	story := &apiv2.StoryResponse{
		Service: "payment-service",
		Anchor:  &apiv2.StoryAnchor{Step: "charge"},
		Path:    []apiv2.StoryStep{{Name: "charge", Status: "error", ErrorCode: "DB_TIMEOUT"}},
	}
	p := newPropagationEvidence(story, "tx", nil, time.Now())
	if p.CaptureStatus != CapturePartial {
		t.Errorf("CaptureStatus = %s; want partial (FirstSeenAt nil)", p.CaptureStatus)
	}
	if len(p.Path) != 1 || p.Path[0].Step != "charge" {
		t.Errorf("Path lost: %+v", p.Path)
	}
}
