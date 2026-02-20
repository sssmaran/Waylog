package eventlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PruneOlderThan deletes event log files older than maxAge.
// activeFile is the path of the currently open writer file — it is never deleted.
// Returns the number of files deleted.
func PruneOlderThan(dir string, maxAge time.Duration, activeFile string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("eventlog: read dir %s: %w", dir, err)
	}

	cutoff := time.Now().Add(-maxAge)
	deleted := 0

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || !strings.HasPrefix(e.Name(), "events-") {
			continue
		}

		path := filepath.Join(dir, e.Name())

		// Never delete the active writer's file.
		if path == activeFile {
			continue
		}

		ts, ok := parseFileTimestamp(e.Name())
		if !ok {
			continue
		}

		if ts.Before(cutoff) {
			if err := os.Remove(path); err != nil {
				return deleted, fmt.Errorf("eventlog: remove %s: %w", path, err)
			}
			deleted++
		}
	}

	return deleted, nil
}

// parseFileTimestamp extracts the timestamp from a filename like
// "events-20260220-091300.jsonl" or "events-20260220-091300-1.jsonl" (rotation sequence).
func parseFileTimestamp(name string) (time.Time, bool) {
	name = strings.TrimPrefix(name, "events-")
	name = strings.TrimSuffix(name, ".jsonl")
	// Strip optional sequence suffix (e.g. "-1", "-2").
	if i := strings.LastIndex(name, "-"); i > 0 {
		candidate := name[:i]
		if _, err := time.Parse("20060102-150405", candidate); err == nil {
			name = candidate
		}
	}
	t, err := time.Parse("20060102-150405", name)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
