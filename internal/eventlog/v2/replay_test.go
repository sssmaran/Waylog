package eventlogv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplayWalksOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeReplayFile(t, dir, "events-20260428-010000.jsonl", now.Add(-3*time.Hour), "a", "b")
	writeReplayFile(t, dir, "events-20260428-020000.jsonl", now.Add(-2*time.Hour), "c", "d")
	writeReplayFile(t, dir, "events-20260428-030000.jsonl", now.Add(-1*time.Hour), "e", "f")

	var ids []string
	count, err := Replay(dir, time.Time{}, func(raw []byte) error {
		var v map[string]string
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v["event_id"])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 || strings.Join(ids, ",") != "a,b,c,d,e,f" {
		t.Fatalf("count=%d ids=%v", count, ids)
	}
}

func TestReplayEmptyDir(t *testing.T) {
	count, err := Replay(t.TempDir(), time.Time{}, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d want 0", count)
	}
}

func TestReplayAcceptsOneMBLine(t *testing.T) {
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

	count, err := Replay(dir, time.Time{}, func(raw []byte) error {
		if len(raw) != len(withPadding) {
			t.Fatalf("line=%d want %d", len(raw), len(withPadding))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestReplaySkipsOversizedLineAndContinues(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", maxReplayLineBytes+1) + "\n" + replayEventJSON(t, "a") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events-20260428-010000.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	count, err := Replay(dir, time.Time{}, func(raw []byte) error {
		if !strings.Contains(string(raw), `"event_id":"a"`) {
			t.Fatalf("raw=%s", raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestReplaySkipsOldFilesBySince(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeReplayFile(t, dir, "events-20260428-010000.jsonl", now.Add(-2*time.Hour), "old")
	writeReplayFile(t, dir, "events-20260428-020000.jsonl", now, "new")

	var ids []string
	count, err := Replay(dir, now.Add(-time.Hour), func(raw []byte) error {
		var v map[string]string
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v["event_id"])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(ids) != 1 || ids[0] != "new" {
		t.Fatalf("count=%d ids=%v", count, ids)
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
