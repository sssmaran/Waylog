package ingestv2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	eventlogv2 "github.com/sssmaran/WaylogCLI/internal/eventlog/v2"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestReplayWALRebuildsDedupAndIndex(t *testing.T) {
	dir := t.TempDir()
	writeV2ReplayFile(t, dir, "events-20260428-010000.jsonl", time.Now(), []string{
		validEventJSON("00000000-0000-4000-8000-000000000001"),
		validEventJSON("00000000-0000-4000-8000-000000000002"),
	})
	dedup := NewDedup(10, nil)
	idx := NewRecentIndex(nil)

	res, err := ReplayWAL(dir, dedup, NewProjector(idx), time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.DedupLoaded != 2 || res.Projected != 2 {
		t.Fatalf("replay=%+v", res)
	}
	if !dedup.Seen("00000000-0000-4000-8000-000000000001") {
		t.Fatal("dedup not warmed")
	}
	if _, ok := idx.GetByID("00000000-0000-4000-8000-000000000002"); !ok {
		t.Fatal("event not indexed")
	}
}

func TestReplayWALSkipsBadLinesAndContinues(t *testing.T) {
	dir := t.TempDir()
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	raw := validEventMap("00000000-0000-4000-8000-000000000001")
	delete(raw, "service")
	writeV2ReplayFile(t, dir, "events-20260428-010000.jsonl", time.Now(), []string{
		"{bad",
		strings.Repeat("x", maxBodyBytes+2),
		mustJSON(t, raw),
		validEventJSON("00000000-0000-4000-8000-000000000002"),
	})
	idx := NewRecentIndex(nil)

	res, err := ReplayWAL(dir, NewDedup(10, nil), NewProjector(idx), time.Time{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if res.Projected != 1 {
		t.Fatalf("projected=%d want 1", res.Projected)
	}
	if _, ok := idx.GetByID("00000000-0000-4000-8000-000000000002"); !ok {
		t.Fatal("valid event not indexed")
	}
	fm := gatherMap(t, reg)
	if got := counterWithLabel(fm["waylog_v2_replay_skipped_total"], "reason", "malformed_json"); got != 1 {
		t.Fatalf("malformed_json skips=%v want 1", got)
	}
	if got := counterWithLabel(fm["waylog_v2_replay_skipped_total"], "reason", "schema_invalid"); got != 1 {
		t.Fatalf("schema_invalid skips=%v want 1", got)
	}
}

func TestReplayWALSkipsOldFilesAndOldEvents(t *testing.T) {
	dir := t.TempDir()
	cutoff := time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)
	writeV2ReplayFile(t, dir, "events-20260428-010000.jsonl", cutoff.Add(-10*time.Minute), []string{
		validEventJSON("00000000-0000-4000-8000-000000000001"),
	})
	oldEvent := validEventMap("00000000-0000-4000-8000-000000000002")
	oldEvent["ts_start"] = "2026-04-25T10:00:00.000Z"
	oldEvent["ts_end"] = "2026-04-25T10:00:00.010Z"
	writeV2ReplayFile(t, dir, "events-20260428-020000.jsonl", cutoff.Add(10*time.Minute), []string{
		mustJSON(t, oldEvent),
		validEventJSON("00000000-0000-4000-8000-000000000003"),
	})
	idx := NewRecentIndex(nil)

	res, err := ReplayWAL(dir, NewDedup(10, nil), NewProjector(idx), cutoff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Projected != 1 {
		t.Fatalf("projected=%d want 1", res.Projected)
	}
	if _, ok := idx.GetByID("00000000-0000-4000-8000-000000000003"); !ok {
		t.Fatal("new event not indexed")
	}
	if _, ok := idx.GetByID("00000000-0000-4000-8000-000000000002"); ok {
		t.Fatal("old event should be skipped")
	}
}

func TestReplayWALPreservesNewestAtDedupCapacity(t *testing.T) {
	dir := t.TempDir()
	writeV2ReplayFile(t, dir, "events-20260428-010000.jsonl", time.Now(), []string{
		validEventJSON("00000000-0000-4000-8000-000000000001"),
		validEventJSON("00000000-0000-4000-8000-000000000002"),
		validEventJSON("00000000-0000-4000-8000-000000000003"),
	})
	dedup := NewDedup(2, nil)

	if _, err := ReplayWAL(dir, dedup, NewProjector(NewRecentIndex(nil)), time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if dedup.Seen("00000000-0000-4000-8000-000000000001") {
		t.Fatal("oldest id should be evicted")
	}
	if !dedup.Seen("00000000-0000-4000-8000-000000000002") || !dedup.Seen("00000000-0000-4000-8000-000000000003") {
		t.Fatal("newest ids should remain")
	}
}

func TestReplayWALRebuildsEquivalentReadAPIsAndDedupe(t *testing.T) {
	dir := t.TempDir()
	wal, err := eventlogv2.New(dir, eventlogv2.WithSync(false))
	if err != nil {
		t.Fatalf("eventlogv2.New: %v", err)
	}
	liveDedup := NewDedup(32, nil)
	liveIndex := NewRecentIndex(nil)
	liveHandler := newTestHandlerWithConfig(t, Config{Dedup: liveDedup, WAL: wal, Index: liveIndex})

	bodies := []string{
		mustJSON(t, cascadeGatewayEvent()),
		mustJSON(t, cascadeCheckoutFailureEvent()),
		mustJSON(t, happyCheckoutEvent()),
		mustJSON(t, suppressedPaymentEvent()),
	}
	env, err := liveHandler.IngestRaw(context.Background(), byteBodies(bodies), true)
	if err != nil {
		t.Fatalf("IngestRaw: %v", err)
	}
	if env.Accepted != len(bodies) || env.Duplicate != 0 || len(env.Rejected) != 0 {
		t.Fatalf("env=%+v", env)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("close WAL: %v", err)
	}

	replayDedup := NewDedup(32, nil)
	replayIndex := NewRecentIndex(nil)
	res, err := ReplayWAL(dir, replayDedup, NewProjector(replayIndex), time.Time{}, nil)
	if err != nil {
		t.Fatalf("ReplayWAL: %v", err)
	}
	if res.Projected != len(bodies) || res.DedupLoaded != len(bodies) {
		t.Fatalf("replay=%+v", res)
	}

	assertReaderEquivalent(t, NewReader(liveIndex), NewReader(replayIndex))

	replayHandler := newTestHandlerWithConfig(t, Config{Dedup: replayDedup, WAL: &fakeWAL{}, Index: replayIndex})
	dupEnv, err := replayHandler.IngestRaw(context.Background(), [][]byte{[]byte(mustJSON(t, cascadeCheckoutFailureEvent()))}, true)
	if err != nil {
		t.Fatalf("duplicate IngestRaw: %v", err)
	}
	if dupEnv.Accepted != 0 || dupEnv.Duplicate != 1 || len(dupEnv.Rejected) != 0 {
		t.Fatalf("duplicate env=%+v", dupEnv)
	}
}

func writeV2ReplayFile(t *testing.T, dir, name string, modTime time.Time, lines []string) {
	t.Helper()
	body := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func assertReaderEquivalent(t *testing.T, live, replay *Reader) {
	t.Helper()
	filter := SearchFilter{Since: testTime(-1), Until: testTime(20)}
	blastKey := BlastKeyMode{Key: BlastKey{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"}}

	checks := []struct {
		name   string
		live   any
		replay any
	}{
		{"recent", live.RecentTraces(filter, nil, 10), replay.RecentTraces(filter, nil, 10)},
		{"errors", live.Errors(filter, nil, 10), replay.Errors(filter, nil, 10)},
		{"story", mustStory(t, live, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), mustStory(t, replay, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
		{"blast", live.BlastRadius(filter, blastKey), replay.BlastRadius(filter, blastKey)},
		{"event", mustEvent(t, live, "00000000-0000-4000-8000-000000000102"), mustEvent(t, replay, "00000000-0000-4000-8000-000000000102")},
		{"trace", mustTrace(t, live, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), mustTrace(t, replay, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
	}
	for _, check := range checks {
		if !reflect.DeepEqual(check.live, check.replay) {
			t.Fatalf("%s mismatch\nlive=%s\nreplay=%s", check.name, stableJSON(t, check.live), stableJSON(t, check.replay))
		}
	}
}

func mustStory(t *testing.T, r *Reader, traceID string) StoryResponse {
	t.Helper()
	story, ok := r.TraceStoryByTraceID(traceID)
	if !ok {
		t.Fatalf("story %s not found", traceID)
	}
	return story
}

func mustEvent(t *testing.T, r *Reader, eventID string) *eventv2.Event {
	t.Helper()
	ev, ok := r.GetEvent(eventID)
	if !ok {
		t.Fatalf("event %s not found", eventID)
	}
	return ev
}

func mustTrace(t *testing.T, r *Reader, traceID string) TraceGetResult {
	t.Helper()
	trace, ok := r.GetTrace(traceID)
	if !ok {
		t.Fatalf("trace %s not found", traceID)
	}
	return trace
}

func stableJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byteBodies(lines []string) [][]byte {
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		out = append(out, []byte(line))
	}
	return out
}

func cascadeGatewayEvent() map[string]any {
	raw := validEventMap("00000000-0000-4000-8000-000000000101")
	raw["trace_id"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw["service"] = "api-gateway"
	raw["span_id"] = "1111111111111111"
	raw["status"] = "ok"
	raw["ts_start"] = testTime(1).Format(time.RFC3339Nano)
	raw["ts_end"] = testTime(1).Add(90 * time.Millisecond).Format(time.RFC3339Nano)
	raw["duration_ms"] = 90
	raw["steps"] = []any{
		map[string]any{
			"name":        "checkout.purchase",
			"span_id":     "2222222222222222",
			"start_ms":    2,
			"duration_ms": 80,
			"status":      "ok",
			"downstream":  map[string]any{"service": "checkout", "endpoint": "/checkout", "kind": "http"},
		},
	}
	raw["fields"] = map[string]any{
		"http": map[string]any{"method": "POST", "route": "/purchase", "status": 502},
		"user": map[string]any{"id": "u-001"},
	}
	return raw
}

func cascadeCheckoutFailureEvent() map[string]any {
	raw := validEventMap("00000000-0000-4000-8000-000000000102")
	raw["trace_id"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw["service"] = "checkout"
	raw["span_id"] = "3333333333333333"
	raw["parent_span_id"] = "2222222222222222"
	raw["status"] = "error"
	raw["ts_start"] = testTime(2).Format(time.RFC3339Nano)
	raw["ts_end"] = testTime(2).Add(60 * time.Millisecond).Format(time.RFC3339Nano)
	raw["duration_ms"] = 60
	raw["anchor"] = map[string]any{"step": "payment.charge", "error_code": "PMT_502", "kind": "downstream"}
	raw["steps"] = []any{
		map[string]any{"name": "cart.validate", "start_ms": 0, "duration_ms": 1, "status": "ok"},
		map[string]any{"name": "db.load_cart", "span_id": "4444444444444444", "start_ms": 1, "duration_ms": 4, "status": "ok", "downstream": map[string]any{"service": "db", "endpoint": "/cart/X1", "kind": "http"}},
		map[string]any{"name": "inventory.reserve", "start_ms": 5, "duration_ms": 2, "status": "ok"},
		map[string]any{"name": "payment.charge", "span_id": "5555555555555555", "start_ms": 7, "duration_ms": 35, "status": "error", "downstream": map[string]any{"service": "payment", "endpoint": "/charge", "kind": "http"}, "error": map[string]any{"code": "PMT_502", "reason": "upstream gateway 5xx"}},
	}
	raw["logs"] = []any{
		map[string]any{"ts_offset_ms": 5, "level": "info", "msg": "inventory reserved"},
		map[string]any{"ts_offset_ms": 40, "level": "warn", "msg": "retrying payment"},
		map[string]any{"ts_offset_ms": 42, "level": "error", "msg": "upstream gateway 5xx"},
	}
	raw["fields"] = map[string]any{
		"http": map[string]any{"method": "POST", "route": "/checkout", "status": 502},
		"user": map[string]any{"id": "u-001"},
	}
	raw["errors"] = []any{map[string]any{"code": "PMT_502", "reason": "upstream gateway 5xx"}}
	return raw
}

func happyCheckoutEvent() map[string]any {
	raw := validEventMap("00000000-0000-4000-8000-000000000103")
	raw["trace_id"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw["service"] = "checkout"
	raw["ts_start"] = testTime(3).Format(time.RFC3339Nano)
	raw["ts_end"] = testTime(3).Add(20 * time.Millisecond).Format(time.RFC3339Nano)
	raw["duration_ms"] = 20
	raw["fields"] = map[string]any{
		"http": map[string]any{"method": "POST", "route": "/checkout", "status": 200},
		"user": map[string]any{"id": "u-002"},
	}
	return raw
}

func suppressedPaymentEvent() map[string]any {
	raw := validEventMap("00000000-0000-4000-8000-000000000104")
	raw["trace_id"] = "cccccccccccccccccccccccccccccccc"
	raw["service"] = "payment"
	raw["status"] = "suppressed"
	raw["ts_start"] = testTime(4).Format(time.RFC3339Nano)
	raw["ts_end"] = testTime(4).Add(10 * time.Millisecond).Format(time.RFC3339Nano)
	raw["duration_ms"] = 10
	raw["steps"] = []any{}
	raw["logs"] = []any{}
	raw["fields"] = map[string]any{
		"http": map[string]any{"method": "POST", "route": "/charge", "status": 502},
		"user": map[string]any{"id": "u-003"},
	}
	return raw
}
