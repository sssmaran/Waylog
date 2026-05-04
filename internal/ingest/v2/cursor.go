package ingestv2

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

var errInvalidCursor = errors.New("invalid cursor")

type EventCursor struct {
	TsNano  int64  `json:"t"`
	EventID string `json:"e"`
}

type TraceCursor struct {
	TsNano  int64  `json:"t"`
	TraceID string `json:"r"`
}

type ErrorCursor struct {
	Count     int    `json:"c"`
	Service   string `json:"s"`
	Step      string `json:"p"`
	ErrorCode string `json:"e"`
}

func EncodeEventCursor(c EventCursor) (string, error) {
	return encodeCursor(c)
}

func DecodeEventCursor(s string) (EventCursor, error) {
	var c EventCursor
	if err := decodeCursor(s, &c); err != nil {
		return EventCursor{}, err
	}
	if c.TsNano < 0 || c.EventID == "" {
		return EventCursor{}, errInvalidCursor
	}
	return c, nil
}

func EncodeTraceCursor(c TraceCursor) (string, error) {
	return encodeCursor(c)
}

func DecodeTraceCursor(s string) (TraceCursor, error) {
	var c TraceCursor
	if err := decodeCursor(s, &c); err != nil {
		return TraceCursor{}, err
	}
	if c.TsNano < 0 || c.TraceID == "" {
		return TraceCursor{}, errInvalidCursor
	}
	return c, nil
}

func EncodeErrorCursor(c ErrorCursor) (string, error) {
	return encodeCursor(c)
}

func DecodeErrorCursor(s string) (ErrorCursor, error) {
	var c ErrorCursor
	if err := decodeCursor(s, &c); err != nil {
		return ErrorCursor{}, err
	}
	if c.Count < 0 || c.Service == "" || c.Step == "" || c.ErrorCode == "" {
		return ErrorCursor{}, errInvalidCursor
	}
	return c, nil
}

func encodeCursor(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(s string, out any) error {
	if s == "" {
		return errInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(s)
	}
	if err != nil {
		return errInvalidCursor
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errInvalidCursor
	}
	return nil
}

func afterEventCursor(evTsNano int64, eventID string, cursor *EventCursor) bool {
	if cursor == nil {
		return true
	}
	return evTsNano < cursor.TsNano || (evTsNano == cursor.TsNano && eventID > cursor.EventID)
}

func afterTraceCursor(tsNano int64, traceID string, cursor *TraceCursor) bool {
	if cursor == nil {
		return true
	}
	return tsNano < cursor.TsNano || (tsNano == cursor.TsNano && traceID > cursor.TraceID)
}

func afterErrorCursor(count int, key ErrorKey, cursor *ErrorCursor) bool {
	if cursor == nil {
		return true
	}
	if count != cursor.Count {
		return count < cursor.Count
	}
	if key.Service != cursor.Service {
		return key.Service > cursor.Service
	}
	if key.Step != cursor.Step {
		return key.Step > cursor.Step
	}
	return key.ErrorCode > cursor.ErrorCode
}
