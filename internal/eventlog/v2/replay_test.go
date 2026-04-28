package eventlogv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
)

func TestWarmDedupLoadsOldestFirstAndKeepsNewestAtCapacity(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeReplayFile(t, dir, "events-20260428-010000.jsonl", now.Add(-3*time.Hour), "a", "b")
	writeReplayFile(t, dir, "events-20260428-020000.jsonl", now.Add(-2*time.Hour), "c", "d")
	writeReplayFile(t, dir, "events-20260428-030000.jsonl", now.Add(-1*time.Hour), "e", "f")

	d := ingestv2.NewDedup(4, nil)
	loaded, err := WarmDedup(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 6 {
		t.Fatalf("loaded=%d want 6", loaded)
	}
	for _, id := range []string{"a", "b"} {
		if d.Seen(id) {
			t.Fatalf("%s should have been evicted", id)
		}
	}
	for _, id := range []string{"c", "d", "e", "f"} {
		if !d.Seen(id) {
			t.Fatalf("%s should be present", id)
		}
	}
}

func TestWarmDedupSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events-20260428-010000.jsonl")
	body := strings.Join([]string{
		replayEventJSON(t, "a"),
		"{bad",
		`{"schema_version":"2.0"}`,
		replayEventJSON(t, "b"),
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	d := ingestv2.NewDedup(10, nil)
	loaded, err := WarmDedup(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 2 || !d.Seen("a") || !d.Seen("b") {
		t.Fatalf("loaded=%d seen(a)=%v seen(b)=%v", loaded, d.Seen("a"), d.Seen("b"))
	}
}

func TestWarmDedupEmptyDir(t *testing.T) {
	loaded, err := WarmDedup(t.TempDir(), ingestv2.NewDedup(10, nil))
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 0 {
		t.Fatalf("loaded=%d want 0", loaded)
	}
}

func TestWarmDedupAcceptsOneMBLine(t *testing.T) {
	dir := t.TempDir()
	raw := replayEventJSON(t, "a")
	paddingLen := (1 << 20) - len(raw) - len(`,"padding":""`)
	if paddingLen < 0 {
		t.Fatalf("base fixture too large: %d", len(raw))
	}
	withPadding := strings.TrimSuffix(raw, "}") + `,"padding":"` + strings.Repeat("a", paddingLen) + `"}`
	if len(withPadding) > 1<<20 {
		t.Fatalf("line=%d want <=1MB", len(withPadding))
	}
	if err := os.WriteFile(filepath.Join(dir, "events-20260428-010000.jsonl"), []byte(withPadding+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := ingestv2.NewDedup(10, nil)
	loaded, err := WarmDedup(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 1 || !d.Seen("a") {
		t.Fatalf("loaded=%d seen=%v", loaded, d.Seen("a"))
	}
}

func TestWarmDedupSkipsOversizedLineAndContinues(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", maxReplayLineBytes+1) + "\n" + replayEventJSON(t, "a") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events-20260428-010000.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	d := ingestv2.NewDedup(10, nil)
	loaded, err := WarmDedup(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 1 || !d.Seen("a") {
		t.Fatalf("loaded=%d seen=%v", loaded, d.Seen("a"))
	}
}

func writeReplayFile(t *testing.T, dir, name string, modTime time.Time, ids ...string) {
	t.Helper()
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(replayEventJSON(t, id))
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func replayEventJSON(t *testing.T, id string) string {
	t.Helper()
	raw := map[string]any{"event_id": id}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
