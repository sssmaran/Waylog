package eventlog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// SearchFilter defines criteria for searching raw event logs.
// All non-zero fields are ANDed together.
type SearchFilter struct {
	TraceID   string
	UserID    string
	Service   string
	ErrorCode string
	Start     time.Time
	End       time.Time
	Limit     int // default 50, max 200
}

// Matches returns true if ev satisfies all non-empty filter fields.
func (f *SearchFilter) Matches(ev *event.WideEvent) bool {
	if !f.Start.IsZero() && ev.Timestamp.Before(f.Start) {
		return false
	}
	if !f.End.IsZero() && ev.Timestamp.After(f.End) {
		return false
	}
	if f.TraceID != "" && ev.Request.TraceID != f.TraceID {
		return false
	}
	if f.UserID != "" && ev.User.ID != f.UserID {
		return false
	}
	if f.Service != "" && ev.System.Service != f.Service {
		return false
	}
	if f.ErrorCode != "" && (ev.Error == nil || ev.Error.Code != f.ErrorCode) {
		return false
	}
	return true
}

// Search reads .jsonl files in dir and returns events matching the filter,
// sorted by timestamp descending. Scans all files, then truncates to limit.
func Search(dir string, f SearchFilter) ([]event.WideEvent, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

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
		logEntries, err := ReadFile(path)
		if err != nil {
			continue
		}
		for i := range logEntries {
			if f.Matches(&logEntries[i].Event) {
				result = append(result, logEntries[i].Event)
			}
		}
	}

	// Sort by timestamp descending, then truncate to limit
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	if len(result) > f.Limit {
		result = result[:f.Limit]
	}

	return result, nil
}
