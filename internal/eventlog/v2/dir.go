package eventlogv2

import (
	"path/filepath"
	"strings"
)

// ResolveDir resolves the v2 WAL directory the way the whole system does: the
// explicit EVENT_LOG_V2_DIR value (v2Dir) when set, otherwise the default
// derived from EVENT_LOG_DIR (baseDir). Both cmd/ingest (the server) and
// `waylog doctor` call this so their resolution can never drift.
func ResolveDir(v2Dir, baseDir string) string {
	if d := strings.TrimSpace(v2Dir); d != "" {
		return d
	}
	return DefaultDir(strings.TrimSpace(baseDir))
}

// DefaultDir resolves the default v2 WAL directory from EVENT_LOG_DIR: it nests
// "v2" under the configured event-log dir, or uses ./data/eventlog-v2 when no
// event-log dir is set.
func DefaultDir(eventLogDir string) string {
	if eventLogDir != "" {
		return filepath.Join(eventLogDir, "v2")
	}
	return "./data/eventlog-v2"
}
