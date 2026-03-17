package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TestParseRef_Valid
// ---------------------------------------------------------------------------

func TestParseRef_Valid(t *testing.T) {
	cases := []struct {
		input    string
		stepID   string
		pathKind []string // "field" or "index" per segment
		fields   []string // field names (parallel to pathKind)
		indices  []int    // array indices (parallel to pathKind)
	}{
		{
			input:  `$steps["step1"].result`,
			stepID: "step1",
		},
		{
			input:    `$steps["step1"].result.trace_id`,
			stepID:   "step1",
			pathKind: []string{"field"},
			fields:   []string{"trace_id"},
		},
		{
			input:    `$steps["step_a"].result.items[0]`,
			stepID:   "step_a",
			pathKind: []string{"field", "index"},
			fields:   []string{"items", ""},
			indices:  []int{0, 0},
		},
		{
			input:    `$steps["step_a"].result.items[0].name`,
			stepID:   "step_a",
			pathKind: []string{"field", "index", "field"},
			fields:   []string{"items", "", "name"},
			indices:  []int{0, 0, 0},
		},
		{
			input:    `$steps["x"].result[0]`,
			stepID:   "x",
			pathKind: []string{"index"},
			indices:  []int{0},
		},
		{
			input:    `$steps["abc"].result.a.b.c`,
			stepID:   "abc",
			pathKind: []string{"field", "field", "field"},
			fields:   []string{"a", "b", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			ref, err := ParseRef(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.StepID != tc.stepID {
				t.Errorf("StepID: got %q want %q", ref.StepID, tc.stepID)
			}
			if len(ref.Path) != len(tc.pathKind) {
				t.Fatalf("path len: got %d want %d", len(ref.Path), len(tc.pathKind))
			}
			for i, seg := range ref.Path {
				if seg.Kind != tc.pathKind[i] {
					t.Errorf("seg[%d].Kind: got %q want %q", i, seg.Kind, tc.pathKind[i])
				}
				if seg.Kind == "field" && len(tc.fields) > i && tc.fields[i] != "" {
					if seg.Field != tc.fields[i] {
						t.Errorf("seg[%d].Field: got %q want %q", i, seg.Field, tc.fields[i])
					}
				}
				if seg.Kind == "index" && len(tc.indices) > i {
					if seg.Index != tc.indices[i] {
						t.Errorf("seg[%d].Index: got %d want %d", i, seg.Index, tc.indices[i])
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestParseRef_Invalid
// ---------------------------------------------------------------------------

func TestParseRef_Invalid(t *testing.T) {
	cases := []struct {
		input   string
		errFrag string
	}{
		{"", "must start with"},
		{"steps[\"x\"].result", "must start with"},
		{`$steps[0].result`, "INVALID_PLAN_REF"},
		{`$steps[""].result`, "empty step ID"},
		{`$steps["x"]`, "must have .result"},
		{`$steps["x"].result.`, "trailing dot"},
		{`$steps["x"].result[-1]`, "invalid array index"},
		{`$steps["x"].result[*]`, "invalid array index"},
		{`$steps["x"].result.1bad`, "invalid field name"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			_, err := ParseRef(tc.input)
			if err == nil {
				t.Fatalf("expected error for input %q, got nil", tc.input)
			}
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestFindRefs
// ---------------------------------------------------------------------------

func TestFindRefs(t *testing.T) {
	params := json.RawMessage(`{"trace_id": "$steps[\"fetch\"].result.id"}`)
	refs, err := FindRefs(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Pointer != "/trace_id" {
		t.Errorf("Pointer: got %q want %q", refs[0].Pointer, "/trace_id")
	}
	if refs[0].Ref.StepID != "fetch" {
		t.Errorf("StepID: got %q want %q", refs[0].Ref.StepID, "fetch")
	}
	if len(refs[0].Ref.Path) != 1 || refs[0].Ref.Path[0].Field != "id" {
		t.Errorf("unexpected path: %+v", refs[0].Ref.Path)
	}
}

func TestFindRefs_MixedString(t *testing.T) {
	params := json.RawMessage(`{"x": "prefix-$steps[\"s\"].result"}`)
	_, err := FindRefs(params)
	if err == nil {
		t.Fatal("expected error for mixed string ref, got nil")
	}
	if !strings.Contains(err.Error(), "mixed") {
		t.Errorf("error should mention 'mixed', got: %v", err)
	}
}

func TestFindRefs_Nested(t *testing.T) {
	params := json.RawMessage(`{"outer": {"inner": "$steps[\"s\"].result"}}`)
	refs, err := FindRefs(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Pointer != "/outer/inner" {
		t.Errorf("Pointer: got %q want /outer/inner", refs[0].Pointer)
	}
}

func TestFindRefs_NoRefs(t *testing.T) {
	params := json.RawMessage(`{"x": "hello", "y": 42}`)
	refs, err := FindRefs(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

// ---------------------------------------------------------------------------
// TestValidateRefPath
// ---------------------------------------------------------------------------

func TestValidateRefPath(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					}
				}
			},
			"count": {"type": "integer"}
		}
	}`)

	cases := []struct {
		name    string
		refStr  string
		wantErr bool
		errFrag string
	}{
		{
			name:   "empty path (full result)",
			refStr: `$steps["s"].result`,
		},
		{
			name:   "top-level field",
			refStr: `$steps["s"].result.count`,
		},
		{
			name:   "nested through array",
			refStr: `$steps["s"].result.items[0].name`,
		},
		{
			name:    "unknown top-level field",
			refStr:  `$steps["s"].result.missing`,
			wantErr: true,
			errFrag: "not found",
		},
		{
			name:    "index on non-array",
			refStr:  `$steps["s"].result.count[0]`,
			wantErr: true,
			errFrag: "non-array",
		},
		{
			name:    "field on integer type",
			refStr:  `$steps["s"].result.count.sub`,
			wantErr: true,
			errFrag: "non-object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseRef(tc.refStr)
			if err != nil {
				t.Fatalf("ParseRef error: %v", err)
			}
			err = ValidateRefPath(ref, schema)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errFrag)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateRefPath_UnsupportedOneOf(t *testing.T) {
	schema := json.RawMessage(`{"oneOf": [{"type": "string"}, {"type": "integer"}]}`)
	ref, err := ParseRef(`$steps["s"].result.field`)
	if err != nil {
		t.Fatalf("ParseRef error: %v", err)
	}
	err = ValidateRefPath(ref, schema)
	if err == nil {
		t.Fatal("expected error for oneOf, got nil")
	}
	if !strings.Contains(err.Error(), "INVALID_PLAN_REF") {
		t.Errorf("expected INVALID_PLAN_REF in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "oneOf") {
		t.Errorf("expected 'oneOf' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestResolveRef
// ---------------------------------------------------------------------------

func TestResolveRef(t *testing.T) {
	results := map[string]json.RawMessage{
		"fetch": json.RawMessage(`{"id": "abc123", "items": [{"name": "first"}, {"name": "second"}]}`),
	}

	t.Run("full result", func(t *testing.T) {
		ref, _ := ParseRef(`$steps["fetch"].result`)
		got, err := ResolveRef(ref, results)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(got), "abc123") {
			t.Errorf("expected abc123 in result, got %s", got)
		}
	})

	t.Run("nested field", func(t *testing.T) {
		ref, _ := ParseRef(`$steps["fetch"].result.id`)
		got, err := ResolveRef(ref, results)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `"abc123"` {
			t.Errorf("got %s, want \"abc123\"", got)
		}
	})

	t.Run("array index", func(t *testing.T) {
		ref, _ := ParseRef(`$steps["fetch"].result.items[1].name`)
		got, err := ResolveRef(ref, results)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `"second"` {
			t.Errorf("got %s, want \"second\"", got)
		}
	})
}

func TestResolveRef_Failures(t *testing.T) {
	results := map[string]json.RawMessage{
		"fetch": json.RawMessage(`{"items": []}`),
	}

	t.Run("missing step", func(t *testing.T) {
		ref, _ := ParseRef(`$steps["other"].result`)
		_, err := ResolveRef(ref, results)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "REF_RESOLVE_FAILED") {
			t.Errorf("expected REF_RESOLVE_FAILED, got: %v", err)
		}
	})

	t.Run("index out of bounds on empty array", func(t *testing.T) {
		ref, _ := ParseRef(`$steps["fetch"].result.items[0]`)
		_, err := ResolveRef(ref, results)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "REF_RESOLVE_FAILED") {
			t.Errorf("expected REF_RESOLVE_FAILED, got: %v", err)
		}
	})

	t.Run("field on non-object", func(t *testing.T) {
		results2 := map[string]json.RawMessage{
			"s": json.RawMessage(`42`),
		}
		ref, _ := ParseRef(`$steps["s"].result.field`)
		_, err := ResolveRef(ref, results2)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "REF_RESOLVE_FAILED") {
			t.Errorf("expected REF_RESOLVE_FAILED, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestResolveParams
// ---------------------------------------------------------------------------

func TestResolveParams(t *testing.T) {
	results := map[string]json.RawMessage{
		"lookup": json.RawMessage(`{"trace_id": "xyz999"}`),
	}

	params := json.RawMessage(`{"trace_id": "$steps[\"lookup\"].result.trace_id", "limit": 10}`)
	refs, err := FindRefs(params)
	if err != nil {
		t.Fatalf("FindRefs error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}

	resolved, err := ResolveParams(params, refs, results)
	if err != nil {
		t.Fatalf("ResolveParams error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(resolved, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out["trace_id"] != "xyz999" {
		t.Errorf("trace_id: got %v, want xyz999", out["trace_id"])
	}
	if out["limit"] != float64(10) {
		t.Errorf("limit: got %v, want 10", out["limit"])
	}
}
