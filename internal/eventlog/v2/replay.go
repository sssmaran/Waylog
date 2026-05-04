package eventlogv2

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxReplayLineBytes = (1 << 20) + 1

func Replay(dir string, since time.Time, fn func(rawLine []byte) error) (int, error) {
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
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
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
		n, err := replayFileLines(file.path, fn)
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

func replayFileLines(path string, fn func(rawLine []byte) error) (int, error) {
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
		if len(line) > 0 && fn != nil {
			if err := fn(line); err != nil {
				return loaded, err
			}
			loaded++
		}
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
		// ReadSlice can return fragments before a newline; keep consuming them
		// even after marking the line too long so the next read starts aligned.
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
