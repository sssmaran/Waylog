package ingest

import (
	"testing"
	"time"
)

func TestRowIDCursorRoundTrip(t *testing.T) {
	encoded := encodeRowIDCursor(42)
	if encoded == "" {
		t.Fatal("expected non-empty cursor")
	}
	id, err := decodeRowIDCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
}

func TestRowIDCursorEmpty(t *testing.T) {
	id, err := decodeRowIDCursor("")
	if err != nil {
		t.Fatalf("empty cursor should not error: %v", err)
	}
	if id != 0 {
		t.Errorf("expected 0 for empty cursor, got %d", id)
	}
}

func TestRowIDCursorInvalid(t *testing.T) {
	cases := []string{"not-base64!!!", "aW52YWxpZA=="}
	for _, c := range cases {
		_, err := decodeRowIDCursor(c)
		if err == nil {
			t.Errorf("expected error for cursor %q", c)
		}
	}
}

func TestTimeCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 3, 16, 12, 0, 0, 123456789, time.UTC)
	traceID := "abc123def456"
	encoded := encodeTimeCursor(ts, traceID)
	if encoded == "" {
		t.Fatal("expected non-empty cursor")
	}
	gotTS, gotID, err := decodeTimeCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("ts = %v, want %v", gotTS, ts)
	}
	if gotID != traceID {
		t.Errorf("traceID = %s, want %s", gotID, traceID)
	}
}

func TestTimeCursorEmpty(t *testing.T) {
	ts, traceID, err := decodeTimeCursor("")
	if err != nil {
		t.Fatalf("empty cursor should not error: %v", err)
	}
	if !ts.IsZero() || traceID != "" {
		t.Errorf("expected zero values for empty cursor")
	}
}

func TestTimeCursorInvalid(t *testing.T) {
	cases := []string{"not-base64!!!", "aW52YWxpZA=="}
	for _, c := range cases {
		_, _, err := decodeTimeCursor(c)
		if err == nil {
			t.Errorf("expected error for cursor %q", c)
		}
	}
}
