package eventv2

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventRoundTrip(t *testing.T) {
	e := Event{
		SchemaVersion: "2.0",
		EventID:       "e1f2c3d4-0000-4000-a000-000000000001",
		TsStart:       time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		TsEnd:         time.Date(2026, 4, 24, 12, 0, 1, 0, time.UTC),
		DurationMS:    1000,
		Kind:          "http",
		Service:       "checkout",
		Env:           "test",
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "fedcba9876543210",
		ParentSpanID:  "",
		Status:        StatusOK,
		Fields:        map[string]any{"http": map[string]any{"route": "/x"}},
	}

	raw, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.EventID != e.EventID || back.Service != e.Service || back.Status != e.Status {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}

func TestStatusConstants(t *testing.T) {
	cases := []Status{StatusOK, StatusError, StatusTimeout, StatusPartial, StatusAborted, StatusSuppressed}
	for _, s := range cases {
		if s == "" {
			t.Fatalf("empty status constant")
		}
	}
}
