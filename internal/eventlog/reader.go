package eventlog

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// ReadFile reads all WideEvents from a single JSONL file.
func ReadFile(path string) ([]event.WideEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []event.WideEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1MB per line
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var ev event.WideEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Warn("eventlog: skipping malformed line", "file", path, "line", lineNum, "err", err)
			continue
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}

// ReadDir reads all .jsonl files in dir, sorted by filename (chronological),
// and returns events whose Timestamp is strictly after `after`.
func ReadDir(dir string, after time.Time) ([]event.WideEvent, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)

	var result []event.WideEvent
	for _, path := range files {
		events, err := ReadFile(path)
		if err != nil {
			slog.Warn("eventlog: error reading file", "path", path, "err", err)
			continue
		}
		for _, ev := range events {
			if ev.Timestamp.After(after) {
				result = append(result, ev)
			}
		}
	}
	return result, nil
}
