package ingest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Ref is a parsed reference to a step result.
// Syntax: $steps["<step_id>"].result[.<field>|[<index>]]*
type Ref struct {
	StepID string
	Path   []PathSegment
}

// PathSegment is a single step in a JSON path.
type PathSegment struct {
	Kind  string // "field" | "index"
	Field string // set when Kind == "field"
	Index int    // set when Kind == "index"
}

// FoundRef is a ref found in a params object, with its JSON Pointer location.
type FoundRef struct {
	Pointer string // JSON Pointer to the field in params (e.g., "/trace_id")
	Ref     Ref
}

const refPrefix = `$steps["`

// ParseRef parses a ref string of the form:
//
//	$steps["<step_id>"].result[.<field>|[<index>]]*
//
// Empty path (just $steps["x"].result) is valid and returns zero-length Path.
func ParseRef(s string) (Ref, error) {
	if !strings.HasPrefix(s, refPrefix) {
		return Ref{}, fmt.Errorf("INVALID_PLAN_REF: ref must start with %s, got %q", refPrefix, s)
	}
	rest := s[len(refPrefix):]

	// Extract step ID between [" and "]
	end := strings.Index(rest, `"]`)
	if end < 0 {
		return Ref{}, fmt.Errorf("INVALID_PLAN_REF: missing closing \"] in ref %q", s)
	}
	stepID := rest[:end]
	if stepID == "" {
		return Ref{}, fmt.Errorf("INVALID_PLAN_REF: empty step ID in ref %q", s)
	}
	rest = rest[end+2:] // skip "]

	// Must be followed by .result
	if !strings.HasPrefix(rest, ".result") {
		return Ref{}, fmt.Errorf("INVALID_PLAN_REF: ref must have .result after step ID, got %q", s)
	}
	rest = rest[len(".result"):]

	// rest is the path after .result: empty, or starts with . or [
	if rest == "" {
		return Ref{StepID: stepID}, nil
	}

	path, err := parsePath(rest, s)
	if err != nil {
		return Ref{}, err
	}
	return Ref{StepID: stepID, Path: path}, nil
}

// parsePath parses a dot/bracket path segment string.
// Must start with '.' or '['.
func parsePath(s, original string) ([]PathSegment, error) {
	var segments []PathSegment
	for len(s) > 0 {
		switch s[0] {
		case '.':
			s = s[1:]
			if s == "" {
				return nil, fmt.Errorf("INVALID_PLAN_REF: trailing dot in ref %q", original)
			}
			// Read field name: [A-Za-z_][A-Za-z0-9_]*
			n := 0
			for n < len(s) && isFieldChar(s[n], n == 0) {
				n++
			}
			if n == 0 {
				return nil, fmt.Errorf("INVALID_PLAN_REF: invalid field name at %q in ref %q", s, original)
			}
			segments = append(segments, PathSegment{Kind: "field", Field: s[:n]})
			s = s[n:]
		case '[':
			s = s[1:]
			end := strings.Index(s, "]")
			if end < 0 {
				return nil, fmt.Errorf("INVALID_PLAN_REF: missing ] in ref %q", original)
			}
			indexStr := s[:end]
			s = s[end+1:]
			idx, err := strconv.Atoi(indexStr)
			if err != nil || idx < 0 {
				return nil, fmt.Errorf("INVALID_PLAN_REF: invalid array index %q in ref %q", indexStr, original)
			}
			segments = append(segments, PathSegment{Kind: "index", Index: idx})
		default:
			return nil, fmt.Errorf("INVALID_PLAN_REF: unexpected character %q in path of ref %q", s[0], original)
		}
	}
	return segments, nil
}

func isFieldChar(c byte, first bool) bool {
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' {
		return true
	}
	if !first && c >= '0' && c <= '9' {
		return true
	}
	return false
}

// FindRefs scans a params json.RawMessage and returns all FoundRef entries.
// Only pure-ref string values are accepted; mixed strings like "prefix-$steps[...]"
// are rejected with an error.
func FindRefs(params json.RawMessage) ([]FoundRef, error) {
	var root any
	if err := json.Unmarshal(params, &root); err != nil {
		return nil, fmt.Errorf("FindRefs: invalid JSON: %w", err)
	}
	var refs []FoundRef
	if err := walkForRefs(root, "", &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func walkForRefs(v any, pointer string, refs *[]FoundRef) error {
	switch val := v.(type) {
	case string:
		if strings.Contains(val, refPrefix) {
			if !strings.HasPrefix(val, refPrefix) {
				return fmt.Errorf("INVALID_PLAN_REF: mixed string ref not allowed at %q: %q", pointer, val)
			}
			ref, err := ParseRef(val)
			if err != nil {
				return err
			}
			*refs = append(*refs, FoundRef{Pointer: pointer, Ref: ref})
		}
	case map[string]any:
		for k, child := range val {
			childPtr := pointer + "/" + jsonPointerEscape(k)
			if err := walkForRefs(child, childPtr, refs); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range val {
			childPtr := pointer + "/" + strconv.Itoa(i)
			if err := walkForRefs(child, childPtr, refs); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonPointerEscape escapes a JSON key for use in a JSON Pointer (RFC 6901).
func jsonPointerEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

// ValidateRefPath walks a JSON Schema (with $ref already inlined) to validate
// that the ref path is reachable. An empty path is always valid.
// Returns an INVALID_PLAN_REF error for unknown fields, type mismatches, or
// unsupported combiners (oneOf/anyOf/allOf).
func ValidateRefPath(ref Ref, outputSchema json.RawMessage) error {
	if len(ref.Path) == 0 {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(outputSchema, &schema); err != nil {
		return fmt.Errorf("INVALID_PLAN_REF: invalid output schema: %w", err)
	}
	return validateSchemaPath(schema, ref.Path, ref.StepID)
}

func validateSchemaPath(schema map[string]any, path []PathSegment, stepID string) error {
	// Check for unsupported combiners
	for _, combiner := range []string{"oneOf", "anyOf", "allOf"} {
		if _, ok := schema[combiner]; ok {
			return fmt.Errorf("INVALID_PLAN_REF: step %q output schema uses unsupported combiner %q", stepID, combiner)
		}
	}

	if len(path) == 0 {
		return nil
	}
	seg := path[0]
	rest := path[1:]

	schemaType, _ := schema["type"].(string)

	switch seg.Kind {
	case "field":
		if schemaType != "" && schemaType != "object" {
			return fmt.Errorf("INVALID_PLAN_REF: cannot access field %q on non-object type %q in step %q output", seg.Field, schemaType, stepID)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			// No properties defined — we can't validate further; accept.
			return nil
		}
		child, ok := props[seg.Field]
		if !ok {
			return fmt.Errorf("INVALID_PLAN_REF: field %q not found in step %q output schema", seg.Field, stepID)
		}
		childSchema, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("INVALID_PLAN_REF: invalid schema for field %q in step %q", seg.Field, stepID)
		}
		return validateSchemaPath(childSchema, rest, stepID)

	case "index":
		if schemaType != "" && schemaType != "array" {
			return fmt.Errorf("INVALID_PLAN_REF: cannot index into non-array type %q in step %q output", schemaType, stepID)
		}
		items, ok := schema["items"].(map[string]any)
		if !ok {
			// No items schema — accept.
			return nil
		}
		return validateSchemaPath(items, rest, stepID)
	}
	return nil
}

// ResolveRef walks actual JSON data to resolve a ref at execution time.
// Returns REF_RESOLVE_FAILED for missing step, missing field, index out of
// bounds, or null value.
func ResolveRef(ref Ref, results map[string]json.RawMessage) (json.RawMessage, error) {
	data, ok := results[ref.StepID]
	if !ok {
		return nil, fmt.Errorf("REF_RESOLVE_FAILED: step %q not found in results", ref.StepID)
	}
	if len(ref.Path) == 0 {
		return data, nil
	}
	return walkJSON(data, ref.Path, ref.StepID)
}

func walkJSON(data json.RawMessage, path []PathSegment, stepID string) (json.RawMessage, error) {
	// Check for null
	if string(data) == "null" {
		return nil, fmt.Errorf("REF_RESOLVE_FAILED: encountered null value while resolving path in step %q", stepID)
	}

	if len(path) == 0 {
		return data, nil
	}
	seg := path[0]
	rest := path[1:]

	switch seg.Kind {
	case "field":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, fmt.Errorf("REF_RESOLVE_FAILED: cannot access field %q: not an object in step %q", seg.Field, stepID)
		}
		child, ok := obj[seg.Field]
		if !ok {
			return nil, fmt.Errorf("REF_RESOLVE_FAILED: field %q not found in step %q result", seg.Field, stepID)
		}
		return walkJSON(child, rest, stepID)

	case "index":
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, fmt.Errorf("REF_RESOLVE_FAILED: cannot index: not an array in step %q", stepID)
		}
		if seg.Index >= len(arr) {
			return nil, fmt.Errorf("REF_RESOLVE_FAILED: index %d out of bounds (len %d) in step %q", seg.Index, len(arr), stepID)
		}
		return walkJSON(arr[seg.Index], rest, stepID)
	}
	return nil, fmt.Errorf("REF_RESOLVE_FAILED: unknown segment kind %q", seg.Kind)
}

// ResolveParams substitutes all refs in params and returns the resolved JSON.
// Non-ref fields are preserved unchanged.
func ResolveParams(params json.RawMessage, refs []FoundRef, results map[string]json.RawMessage) (json.RawMessage, error) {
	if len(refs) == 0 {
		return params, nil
	}

	// Unmarshal params into a generic structure for modification.
	var root any
	if err := json.Unmarshal(params, &root); err != nil {
		return nil, fmt.Errorf("ResolveParams: invalid params JSON: %w", err)
	}

	for _, fr := range refs {
		resolved, err := ResolveRef(fr.Ref, results)
		if err != nil {
			return nil, err
		}
		// Unmarshal the resolved value to any for embedding.
		var resolvedVal any
		if err := json.Unmarshal(resolved, &resolvedVal); err != nil {
			return nil, fmt.Errorf("ResolveParams: invalid resolved value: %w", err)
		}
		if err := setAtPointer(&root, fr.Pointer, resolvedVal); err != nil {
			return nil, fmt.Errorf("ResolveParams: cannot set at %q: %w", fr.Pointer, err)
		}
	}

	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("ResolveParams: marshal error: %w", err)
	}
	return out, nil
}

// setAtPointer sets the value at the given JSON Pointer path within root.
// root must be a pointer to any (map or slice chain).
func setAtPointer(root *any, pointer string, value any) error {
	if pointer == "" {
		*root = value
		return nil
	}
	// Split pointer into tokens (skip leading /)
	tokens := strings.Split(pointer[1:], "/")
	return setNested(root, tokens, value)
}

func setNested(node *any, tokens []string, value any) error {
	if len(tokens) == 0 {
		*node = value
		return nil
	}
	token := jsonPointerUnescape(tokens[0])
	rest := tokens[1:]

	switch v := (*node).(type) {
	case map[string]any:
		if len(rest) == 0 {
			v[token] = value
		} else {
			child := v[token]
			if err := setNested(&child, rest, value); err != nil {
				return err
			}
			v[token] = child
		}
		return nil
	case []any:
		idx, err := strconv.Atoi(token)
		if err != nil || idx < 0 || idx >= len(v) {
			return fmt.Errorf("index %q out of range", token)
		}
		if len(rest) == 0 {
			v[idx] = value
		} else {
			child := v[idx]
			if err := setNested(&child, rest, value); err != nil {
				return err
			}
			v[idx] = child
		}
		return nil
	}
	return fmt.Errorf("cannot traverse into %T at token %q", *node, token)
}

func jsonPointerUnescape(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	s = strings.ReplaceAll(s, "~0", "~")
	return s
}
