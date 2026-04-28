package eventlogv2

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
)

const maxReplayLineBytes = (1 << 20) + 1

func WarmDedup(dir string, d *ingestv2.Dedup) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	files := make([]replayFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "events-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		files = append(files, replayFile{
			path:    filepath.Join(dir, entry.Name()),
			name:    entry.Name(),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].name < files[j].name
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	loaded := 0
	for _, file := range files {
		n, err := warmFile(file.path, d)
		loaded += n
		if err != nil {
			return loaded, err
		}
	}
	return loaded, nil
}

type replayFile struct {
	path    string
	name    string
	modTime time.Time
}

func warmFile(path string, d *ingestv2.Dedup) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)

	loaded := 0
	lineNum := 0
	for {
		line, tooLong, readErr := readReplayLine(reader)
		if readErr == io.EOF && len(line) == 0 && !tooLong {
			break
		}
		lineNum++
		if tooLong {
			slog.Warn("eventlogv2: skipping oversized line", "file", path, "line", lineNum)
			if readErr == io.EOF {
				break
			}
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return loaded, readErr
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			slog.Warn("eventlogv2: skipping malformed line", "file", path, "line", lineNum, "err", err)
			if readErr == io.EOF {
				break
			}
			continue
		}
		eventID, ok := raw["event_id"].(string)
		if !ok || eventID == "" {
			slog.Warn("eventlogv2: skipping line without event_id", "file", path, "line", lineNum)
			if readErr == io.EOF {
				break
			}
			continue
		}
		d.Add(eventID)
		loaded++
		if readErr == io.EOF {
			break
		}
	}
	return loaded, nil
}

func readReplayLine(r *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		frag, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(frag) > maxReplayLineBytes {
				tooLong = true
				line = nil
			} else {
				line = append(line, frag...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return nil, tooLong, err
		}
		return bytes.TrimRight(line, "\r\n"), tooLong, err
	}
}
