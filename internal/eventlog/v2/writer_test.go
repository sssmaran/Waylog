package eventlogv2

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterWriteRawAppendsNewline(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRaw([]byte("foo")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRaw([]byte("bar\n")); err != nil {
		t.Fatal(err)
	}
	path := w.ActivePath()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "foo\nbar\n" {
		t.Fatalf("content=%q", string(got))
	}
}

func TestWriterRotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, WithMaxBytes(100))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := w.WriteRaw(bytes.Repeat([]byte("a"), 200)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("files=%d want >=2", len(entries))
	}
}

func TestWriterSyncModesAndCloseIdempotent(t *testing.T) {
	for _, syncOnWrite := range []bool{false, true} {
		t.Run(strings.ToLower(boolString(syncOnWrite)), func(t *testing.T) {
			dir := t.TempDir()
			w, err := New(dir, WithSync(syncOnWrite))
			if err != nil {
				t.Fatal(err)
			}
			if err := w.WriteRaw([]byte("foo")); err != nil {
				t.Fatal(err)
			}
			if w.ActivePath() == "" {
				t.Fatal("ActivePath empty before close")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
