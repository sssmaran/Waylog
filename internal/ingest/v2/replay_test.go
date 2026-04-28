package ingestv2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
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
