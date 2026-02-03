package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type TraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Flags        string
}

type ctxKey struct{}

func WithContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

func FromContext(ctx context.Context) (TraceContext, bool) {
	tc, ok := ctx.Value(ctxKey{}).(TraceContext)
	return tc, ok
}

func NewTraceID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return strings.ToLower(hex.EncodeToString(buf))
}

func NewSpanID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return strings.ToLower(hex.EncodeToString(buf))
}

func ParseTraceparent(header string) (traceID string, parentSpanID string, flags string, ok bool) {
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return "", "", "", false
	}
	version := strings.ToLower(parts[0])
	traceID = strings.ToLower(parts[1])
	parentSpanID = strings.ToLower(parts[2])
	flags = strings.ToLower(parts[3])

	if !isLowerHex(version, 2) {
		return "", "", "", false
	}
	if !isLowerHex(traceID, 32) {
		return "", "", "", false
	}
	if !isLowerHex(parentSpanID, 16) {
		return "", "", "", false
	}
	if !isLowerHex(flags, 2) {
		return "", "", "", false
	}

	return traceID, parentSpanID, flags, true
}

func FormatTraceparent(traceID, spanID, flags string) string {
	if flags == "" {
		flags = "01"
	}
	return "00-" + strings.ToLower(traceID) + "-" + strings.ToLower(spanID) + "-" + strings.ToLower(flags)
}

func NormalizeTraceID(value string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if !isLowerHex(v, 32) {
		return "", false
	}
	return v, true
}

func NormalizeSpanID(value string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if !isLowerHex(v, 16) {
		return "", false
	}
	return v, true
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}
