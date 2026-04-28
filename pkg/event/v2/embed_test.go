package eventv2_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

// TestEmbeddedSchemaMatchesDocs guards against drift between the canonical
// docs/schema/v2.0.json and the build-time mirror at pkg/event/v2/v2.0.schema.json.
// The runtime binary uses the embedded copy; updates to the docs copy must
// be mirrored or the runtime validates against a stale schema.
func TestEmbeddedSchemaMatchesDocs(t *testing.T) {
	// Walk up from this package to the repo root, then read docs/schema/v2.0.json.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	docsPath := filepath.Join(repoRoot, "docs", "schema", "v2.0.json")
	docs, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read docs schema: %v", err)
	}
	if !bytes.Equal(docs, eventv2.EmbeddedSchema()) {
		t.Fatalf("embedded schema drifted from %s — mirror docs/schema/v2.0.json into pkg/event/v2/v2.0.schema.json", docsPath)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func TestCompileEmbeddedSchemaSucceeds(t *testing.T) {
	if _, err := eventv2.CompileEmbeddedSchema(); err != nil {
		t.Fatalf("CompileEmbeddedSchema: %v", err)
	}
}
