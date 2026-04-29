package ingestv2

import "testing"

func TestEventCursorRoundTripAndContinuation(t *testing.T) {
	encoded, err := EncodeEventCursor(EventCursor{TsNano: 100, EventID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEventCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.TsNano != 100 || decoded.EventID != "b" {
		t.Fatalf("decoded=%+v", decoded)
	}
	if afterEventCursor(101, "a", &decoded) {
		t.Fatal("newer event should be before cursor")
	}
	if afterEventCursor(100, "a", &decoded) {
		t.Fatal("same timestamp lower event_id should be before cursor")
	}
	if !afterEventCursor(100, "c", &decoded) {
		t.Fatal("same timestamp higher event_id should be after cursor")
	}
	if !afterEventCursor(99, "a", &decoded) {
		t.Fatal("older timestamp should be after cursor")
	}
}

func TestTraceCursorRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"", "not-base64", "bm90LWpzb24", "eyJ0IjotMX0"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := DecodeTraceCursor(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
