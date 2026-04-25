package eventv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestFixtureRoundTripStructural is the Slice 1 parity gate. For each golden
// fixture it: reads the raw JSON, unmarshals into Event, marshals the typed
// value back to JSON, then compares the original and round-tripped payloads
// structurally after masking SDK-runtime-generated fields and normalizing
// semantic-empty values (missing key ≡ "" ≡ [] ≡ {}).
//
// This catches:
//   - JSON-tag drift between Event fields and the schema/fixture shape
//     (renamed tag → field disappears on round-trip)
//   - omitempty on a field that the contract says must always be present
//   - structural divergence introduced by adding a field to Event without
//     also updating fixtures or schema
//
// Byte-stable round-trip is intentionally not the gate — RFC3339 time
// formatting and slice nil/empty distinctions are not stable, but their
// semantic meaning is.
func TestFixtureRoundTripStructural(t *testing.T) {
	fixtures, err := filepath.Glob("../../../testdata/fixtures/v2/*.json")
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures found")
	}

	for _, fp := range fixtures {
		t.Run(filepath.Base(fp), func(t *testing.T) {
			raw, err := os.ReadFile(fp)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			var typed Event
			if err := json.Unmarshal(raw, &typed); err != nil {
				t.Fatalf("unmarshal into Event: %v", err)
			}
			roundTripped, err := json.Marshal(&typed)
			if err != nil {
				t.Fatalf("marshal Event: %v", err)
			}

			origN := normalizeForCompare(t, raw)
			rtN := normalizeForCompare(t, roundTripped)

			if !reflect.DeepEqual(origN, rtN) {
				origPretty, _ := json.MarshalIndent(origN, "", "  ")
				rtPretty, _ := json.MarshalIndent(rtN, "", "  ")
				t.Errorf("structural mismatch for %s\n--- fixture (normalized) ---\n%s\n--- round-trip (normalized) ---\n%s",
					filepath.Base(fp), origPretty, rtPretty)
			}
		})
	}
}

func normalizeForCompare(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("normalize unmarshal: %v", err)
	}
	return normalize(maskNondeterministic(v))
}

// maskNondeterministic replaces SDK-runtime-generated field values with stable
// non-empty sentinels so the parity test gives the same answer for pinned
// fixtures and for live SDK output. The mask uses a *non-empty* sentinel for
// explicit empty-string identity values too — so a root-of-trace
// `parent_span_id: ""` survives the later normalize() step. Without that, a
// round-trip that dropped `parent_span_id` entirely (e.g. via `omitempty`)
// would silently compare equal to a fixture that explicitly set it to "",
// hiding a real schema-contract regression.
func maskNondeterministic(v any) any {
	const (
		sentinelPresent = "__MASKED__"
		sentinelEmpty   = "__MASKED_EMPTY__"
	)
	masked := map[string]struct{}{
		"event_id":       {},
		"ts_start":       {},
		"ts_end":         {},
		"trace_id":       {},
		"span_id":        {}, // also matches steps[].span_id by name
		"parent_span_id": {},
	}
	var walk func(any) any
	walk = func(node any) any {
		switch x := node.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, vv := range x {
				if _, ok := masked[k]; ok {
					if s, isStr := vv.(string); isStr && s == "" {
						out[k] = sentinelEmpty
					} else {
						out[k] = sentinelPresent
					}
					continue
				}
				out[k] = walk(vv)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = walk(e)
			}
			return out
		default:
			return x
		}
	}
	return walk(v)
}

// normalize collapses semantically-empty values (nil, "", [], {}) so that an
// omitempty struct field marshalling away matches a fixture that explicitly
// included an empty array or empty object.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			n := normalize(vv)
			if isSemanticEmpty(n) {
				continue
			}
			out[k] = n
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(x) == 0 {
			return nil
		}
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = normalize(vv)
		}
		return out
	default:
		return x
	}
}

func isSemanticEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	default:
		return false
	}
}
