package incidents

import (
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func testFamily() apiv2.ErrorFamily {
	return apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"}
}

func testIncidentEvent(id, traceID string, ts time.Time, service, step, code, downstream string) *eventv2.Event {
	ev := &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       id,
		TsStart:       ts,
		TsEnd:         ts.Add(10 * time.Millisecond),
		DurationMS:    10,
		Kind:          "http",
		Service:       service,
		Env:           "prod",
		Version:       "v1",
		TraceID:       traceID,
		SpanID:        id + "-span",
		Status:        eventv2.StatusError,
		Anchor:        &eventv2.Anchor{Step: step, ErrorCode: code},
	}
	stepObj := eventv2.Step{Name: step, StartMS: 0, DurationMS: 10, Status: eventv2.StepStatusError, Error: &eventv2.StepError{Code: code, Reason: "failed"}}
	if downstream != "" {
		stepObj.Downstream = &eventv2.Downstream{Service: downstream, Endpoint: "/charge", Kind: "http"}
	}
	ev.Steps = []eventv2.Step{stepObj}
	return ev
}

func testIncident(now time.Time) Incident {
	return Incident{
		IncidentID:       StableID("prod", testFamily(), now),
		Env:              "prod",
		Service:          "checkout",
		ErrorFamily:      testFamily(),
		Status:           StatusActive,
		Cause:            CauseDependency,
		Confidence:       ConfidenceMedium,
		Severity:         7,
		StartedAt:        now,
		UpdatedAt:        now,
		LastSeenAt:       now,
		AffectedRequests: 6,
		AffectedServices: 2,
		TopServices:      []string{"checkout", "payment"},
		SampleTraces:     []string{"trace-a"},
		Evidence:         []Evidence{{Kind: EvidenceTrace, Title: "trace", TraceID: "trace-a", OccurredAt: now}},
		NextChecks:       []string{"check payment"},
		Lift:             6,
		CurrentCount:     6,
	}
}
