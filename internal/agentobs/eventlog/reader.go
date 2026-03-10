package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ReplayStats reports what happened during WAL replay.
type ReplayStats struct {
	FilesRead      int
	FilesErrored   int
	ErroredFiles   []string
	LinesCorrupted int
	EntriesLoaded  int
}

// ReadDir reads all .jsonl files in dir sorted alphabetically.
// Returns entries with LoggedAt after the given time.
func ReadDir(dir string, after time.Time) ([]LogEntry, ReplayStats, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, ReplayStats{}, fmt.Errorf("wal dir %s: %w", dir, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, ReplayStats{}, err
	}
	sort.Strings(matches)

	var (
		all   []LogEntry
		stats ReplayStats
	)

	for _, path := range matches {
		entries, corrupted, err := ReadFile(path)
		if err != nil {
			stats.FilesErrored++
			stats.ErroredFiles = append(stats.ErroredFiles, path)
			slog.Warn("eventlog: file read error", "path", path, "err", err)
			continue
		}
		stats.FilesRead++
		stats.LinesCorrupted += corrupted

		for _, e := range entries {
			if e.LoggedAt.After(after) {
				all = append(all, e)
			}
		}
	}

	stats.EntriesLoaded = len(all)
	return all, stats, nil
}

// ReadFile reads a single JSONL file.
// Returns entries, corrupted line count, and I/O error.
func ReadFile(path string) ([]LogEntry, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var (
		entries   []LogEntry
		corrupted int
	)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		var entry LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			corrupted++
			slog.Warn("eventlog: corrupted line", "path", path, "err", err)
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, corrupted, err
	}
	return entries, corrupted, nil
}

// BuildDedupIndex returns a set of event_ids from log entries.
func BuildDedupIndex(entries []LogEntry) map[string]bool {
	idx := make(map[string]bool, len(entries))
	for _, e := range entries {
		idx[e.Event.EventID] = true
	}
	return idx
}
