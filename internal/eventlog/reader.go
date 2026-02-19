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

// ReadFile reads all LogEntries from a single JSONL file.
// Handles both new LogEntry format and legacy bare WideEvent lines.
func ReadFile(path string) ([]LogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1MB per line
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var entry LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			slog.Warn("eventlog: skipping malformed line", "file", path, "line", lineNum, "err", err)
			continue
		}

		// Backward compat: if logged_at is zero, this is a legacy bare WideEvent line.
		if entry.LoggedAt.IsZero() {
			var ev event.WideEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				slog.Warn("eventlog: skipping malformed legacy line", "file", path, "line", lineNum, "err", err)
				continue
			}
			entry = LogEntry{
				LoggedAt:       ev.Timestamp,
				Event:          ev,
				SampledInGraph: true, // old logs were written post-sampler
			}
		}

		entries = append(entries, entry)
	}
	return entries, sc.Err()
}

// ReadDir reads all .jsonl files in dir, sorted by filename (chronological),
// and returns LogEntries whose LoggedAt is strictly after `after`.
func ReadDir(dir string, after time.Time) ([]LogEntry, error) {
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

	var result []LogEntry
	for _, path := range files {
		fileEntries, err := ReadFile(path)
		if err != nil {
			slog.Warn("eventlog: error reading file", "path", path, "err", err)
			continue
		}
		for _, entry := range fileEntries {
			if entry.LoggedAt.After(after) {
				result = append(result, entry)
			}
		}
	}
	return result, nil
}
