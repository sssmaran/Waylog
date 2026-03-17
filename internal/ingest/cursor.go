package ingest

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// --- RowID cursor (for /v1/events/search, backed by SQLite rowid) ---

func encodeRowIDCursor(id int64) string {
	return base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func decodeRowIDCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor: %w", err)
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor id: %w", err)
	}
	return id, nil
}

// --- Time cursor (for /v1/traces/recent, in-memory graph sorted by timestamp) ---
// Encodes timestamp + trace_id as tiebreaker to avoid skipping entries
// when multiple traces share the same LastSeen value.

func encodeTimeCursor(ts time.Time, traceID string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + traceID
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

func decodeTimeCursor(s string) (time.Time, string, error) {
	if s == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid cursor format")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	return ts, parts[1], nil
}
