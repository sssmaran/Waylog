package eventv2

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFixturesValidateAgainstSchema compiles the v2.0 schema and asserts each
// golden fixture passes validation. Raw fixture files must be schema-valid; the
// parity runner masks non-deterministic runtime fields later during comparison.
func TestFixturesValidateAgainstSchema(t *testing.T) {
	schemaPath, err := filepath.Abs("../../../docs/schema/v2.0.json")
	if err != nil {
		t.Fatalf("abs schema path: %v", err)
	}
	if _, err := os.Stat(schemaPath); err != nil {
		t.Fatalf("schema not found: %v", err)
	}

	matches, err := filepath.Glob("../../../testdata/fixtures/v2/*.json")
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures found")
	}

	for _, fixture := range matches {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			if err := ValidateFile(schemaPath, fixture); err != nil {
				t.Fatalf("fixture %s failed validation: %v", fixture, err)
			}
		})
	}
}
