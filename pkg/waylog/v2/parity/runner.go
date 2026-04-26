// Package parity provides a fixture-driven parity runner that drives the
// Waylog v2 SDK end-to-end through canonical scenarios and compares the
// emitted wide event against the golden fixtures under
// testdata/fixtures/v2/.
//
// The runner uses only the public Waylog SDK surface (Init, Shutdown,
// Begin, Step, Finalize*, From, …) so the test exercises the SDK exactly
// the way an external user would integrate it.
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// fixtureDir is resolved relative to this source file at test time.
// pkg/waylog/v2/parity/ → ../../../../testdata/fixtures/v2/
const fixtureDir = "../../../../testdata/fixtures/v2"

// LoadFixture reads a golden v2 fixture by file name (e.g. "ok-simple.json").
func LoadFixture(name string) ([]byte, error) {
	path := filepath.Join(fixtureDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("parity: load fixture %s: %w", name, err)
	}
	return raw, nil
}

// MaskNonDeterministic returns a JSON blob with SDK-runtime-generated
// identity, timing, and span values replaced by stable sentinels so that
// pinned fixtures and live SDK output can be structurally compared.
//
// Masking targets:
//   - root identity: event_id, trace_id, span_id, parent_span_id
//   - root timing: ts_start, ts_end, duration_ms
//   - per-step: span_id, start_ms, duration_ms
//   - per-log: ts_offset_ms
//
// Empty-string identity values are masked to a separate sentinel so a
// fixture with an explicit `parent_span_id: ""` round-trips distinctly
// from a marshaler that drops the key entirely.
func MaskNonDeterministic(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("parity: unmarshal for mask: %w", err)
	}
	masked := mask(v)
	out, err := json.Marshal(masked)
	if err != nil {
		return nil, fmt.Errorf("parity: marshal masked: %w", err)
	}
	return out, nil
}

// Equal reports whether two JSON blobs are structurally equivalent after
// masking and normalization (empty arrays / objects / strings collapse to
// nil so omitempty marshalling matches fixtures that explicitly set them).
func Equal(a, b []byte) (bool, error) {
	maskedA, err := MaskNonDeterministic(a)
	if err != nil {
		return false, err
	}
	maskedB, err := MaskNonDeterministic(b)
	if err != nil {
		return false, err
	}
	var va, vb any
	if err := json.Unmarshal(maskedA, &va); err != nil {
		return false, fmt.Errorf("parity: unmarshal a: %w", err)
	}
	if err := json.Unmarshal(maskedB, &vb); err != nil {
		return false, fmt.Errorf("parity: unmarshal b: %w", err)
	}
	return reflect.DeepEqual(normalize(va), normalize(vb)), nil
}

const (
	sentinelPresent = "__MASKED__"
	sentinelEmpty   = "__MASKED_EMPTY__"
	sentinelZeroInt = float64(0) // JSON numbers decode to float64
)

var rootStringMask = map[string]struct{}{
	"event_id":       {},
	"trace_id":       {},
	"span_id":        {}, // also masks per-step span_id (handled in walkStep)
	"parent_span_id": {},
	"ts_start":       {},
	"ts_end":         {},
}

var rootIntMask = map[string]struct{}{
	"duration_ms": {},
}

var stepIntMask = map[string]struct{}{
	"start_ms":    {},
	"duration_ms": {},
}

func mask(node any) any {
	root, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := make(map[string]any, len(root))
	for k, v := range root {
		switch {
		case isStringMask(k):
			out[k] = stringMaskValue(v)
		case isRootIntMask(k):
			out[k] = sentinelZeroInt
		case k == "steps":
			out[k] = walkSteps(v)
		case k == "logs":
			out[k] = walkLogs(v)
		default:
			out[k] = v
		}
	}
	return out
}

func isStringMask(k string) bool {
	_, ok := rootStringMask[k]
	return ok
}

func isRootIntMask(k string) bool {
	_, ok := rootIntMask[k]
	return ok
}

func stringMaskValue(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return sentinelEmpty
	}
	return sentinelPresent
}

func walkSteps(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(arr))
	for i, e := range arr {
		out[i] = walkStep(e)
	}
	return out
}

func walkStep(node any) any {
	step, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := make(map[string]any, len(step))
	for k, v := range step {
		switch {
		case k == "span_id":
			out[k] = stringMaskValue(v)
		case isStepIntMask(k):
			out[k] = sentinelZeroInt
		default:
			out[k] = v
		}
	}
	return out
}

func isStepIntMask(k string) bool {
	_, ok := stepIntMask[k]
	return ok
}

func walkLogs(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(arr))
	for i, e := range arr {
		log, ok := e.(map[string]any)
		if !ok {
			out[i] = e
			continue
		}
		entry := make(map[string]any, len(log))
		for k, val := range log {
			if k == "ts_offset_ms" {
				entry[k] = sentinelZeroInt
				continue
			}
			entry[k] = val
		}
		out[i] = entry
	}
	return out
}

// normalize collapses semantically-empty values (nil, "", [], {}) so that
// an omitempty struct field marshalling away matches a fixture that
// explicitly included an empty array or empty object.
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
