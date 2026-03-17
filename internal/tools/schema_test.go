package tools

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
)

func TestOutputSchemas_ValidJSON(t *testing.T) {
	schemas := map[string]string{
		"graph_stats":      graphStatsOutputSchema,
		"explain_request":  explainRequestOutputSchema,
		"trace_graph":      traceGraphOutputSchema,
		"trace_summary":    traceSummaryOutputSchema,
		"graph_failures":   failuresOutputSchema,
		"failure_patterns": patternsOutputSchema,
		"blast_radius":     blastOutputSchema,
		"failure_chain":    chainOutputSchema,
		"graph_query":      queryOutputSchema,
		"compare_windows":  diffOutputSchema,
		"graph_insights":   insightsOutputSchema,
	}

	for name, raw := range schemas {
		t.Run(name, func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal([]byte(raw), &schema); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			// Must have additionalProperties: false
			if ap, ok := schema["additionalProperties"]; !ok || ap != false {
				t.Error("missing or non-false additionalProperties")
			}

			// Must have schema_version in properties
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatal("missing properties")
			}
			if _, ok := props["schema_version"]; !ok {
				t.Error("missing schema_version property")
			}

			// schema_version must be required
			required, ok := schema["required"].([]any)
			if !ok {
				t.Fatal("missing required array")
			}
			found := false
			for _, r := range required {
				if r == "schema_version" {
					found = true
					break
				}
			}
			if !found {
				t.Error("schema_version not in required list")
			}
		})
	}
}

func TestAllToolsHaveVersion(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterGraphTools(reg); err != nil {
		t.Fatal(err)
	}
	for _, tool := range reg.List() {
		if tool.Version == "" {
			t.Errorf("tool %q has no version", tool.Name)
		}
	}
}

// TestHandlerOutputMatchesSchema calls each handler with fixture data and
// validates that output keys, required fields, and types match the declared schema.
func TestHandlerOutputMatchesSchema(t *testing.T) {
	traceID := "0123456789abcdef0123456789abcdef"
	now := time.Now()
	reqID := core.ID("request", traceID)
	spanID := core.ID("span", traceID, "0123456789abcdef")
	userID := core.ID("user", "user-123")
	svcID := core.ID("service", "test-service")
	errID := core.ID("error", "TEST_ERR")

	store := graphstore.NewStore()
	g := core.New()

	g.AddNode(core.Node{
		ID: reqID, Type: core.NodeRequest,
		FirstSeen: now, LastSeen: now,
		Attr: map[string]any{
			"trace_id":    traceID,
			"success":     false,
			"status_code": 500,
			"latency_ms":  int64(42),
			"event_name":  "test-service.error",
			"flow":        "checkout",
			"is_root":     true,
		},
	})
	g.AddNode(core.Node{
		ID: spanID, Type: core.NodeSpan,
		FirstSeen: now, LastSeen: now,
		Attr: map[string]any{
			"trace_id": traceID,
			"span_id":  "0123456789abcdef",
			"service":  "test-service",
		},
	})
	g.AddNode(core.Node{
		ID: userID, Type: core.NodeUser,
		FirstSeen: now, LastSeen: now,
		Attr: map[string]any{"tier": "standard"},
	})
	g.AddNode(core.Node{
		ID: svcID, Type: core.NodeService,
		FirstSeen: now, LastSeen: now,
		Attr: map[string]any{"name": "test-service"},
	})
	g.AddNode(core.Node{
		ID: errID, Type: core.NodeError,
		FirstSeen: now, LastSeen: now,
		Attr: map[string]any{"code": "TEST_ERR", "message": "test error"},
	})

	// Edges
	g.AddEdge(core.Edge{From: reqID, To: userID, Type: core.EdgeRequestBy})
	g.AddEdge(core.Edge{From: reqID, To: svcID, Type: core.EdgeHandledBy})
	g.AddEdge(core.Edge{From: reqID, To: errID, Type: core.EdgeFailedWith})
	g.AddEdge(core.Edge{From: reqID, To: spanID, Type: core.EdgeRequestHasSpan})

	store.Merge(g)

	reg := NewRegistry()
	if err := RegisterGraphTools(reg); err != nil {
		t.Fatal(err)
	}

	// Tool name -> params to call with
	cases := map[string]json.RawMessage{
		"graph_stats":      json.RawMessage(`{}`),
		"explain_request":  json.RawMessage(fmt.Sprintf(`{"trace_id":%q}`, traceID)),
		"trace_graph":      json.RawMessage(fmt.Sprintf(`{"trace_id":%q}`, traceID)),
		"trace_summary":    json.RawMessage(fmt.Sprintf(`{"trace_id":%q}`, traceID)),
		"graph_failures":   json.RawMessage(`{}`),
		"failure_patterns": json.RawMessage(`{}`),
		"blast_radius":     json.RawMessage(`{"error_code":"TEST_ERR"}`),
		"failure_chain":    json.RawMessage(fmt.Sprintf(`{"request_id":%q}`, reqID)),
		"graph_query":      json.RawMessage(`{"expr":"error_code=TEST_ERR","window":"1h"}`),
		"compare_windows":  json.RawMessage(`{"current":"1h","baseline":"1h","offset":"2h"}`),
		"graph_insights":   json.RawMessage(`{}`),
	}

	for _, tool := range reg.List() {
		params, ok := cases[tool.Name]
		if !ok {
			t.Errorf("no test case for tool %q", tool.Name)
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			result, err := reg.Call(t.Context(), store, tool.Name, params)
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			// Marshal result to JSON and back to map
			b, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			var output map[string]any
			if err := json.Unmarshal(b, &output); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			// Parse schema
			var schema map[string]any
			if err := json.Unmarshal(tool.OutputSchema, &schema); err != nil {
				t.Fatalf("parse output schema: %v", err)
			}

			// Validate
			validateObject(t, "", output, schema, schema)
		})
	}
}

// resolveRef resolves a $ref like "#/$defs/span_node" against the root schema.
func resolveRef(schema map[string]any, root map[string]any) map[string]any {
	ref, ok := schema["$ref"].(string)
	if !ok || root == nil {
		return schema
	}
	// Only support "#/$defs/<name>" format
	const prefix = "#/$defs/"
	if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
		return schema
	}
	name := ref[len(prefix):]
	defs, _ := root["$defs"].(map[string]any)
	if resolved, ok := defs[name].(map[string]any); ok {
		return resolved
	}
	return schema
}

// validateObject checks that output matches the schema at the given path.
func validateObject(t *testing.T, path string, output map[string]any, schema map[string]any, root map[string]any) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)

	// Check additionalProperties: false
	if ap, ok := schema["additionalProperties"]; ok && ap == false {
		for key := range output {
			if _, defined := props[key]; !defined {
				t.Errorf("%s: unexpected key %q not in schema properties", path, key)
			}
		}
	}

	// Check required fields
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			key, _ := r.(string)
			if _, exists := output[key]; !exists {
				t.Errorf("%s: required field %q missing from output", path, key)
			}
		}
	}

	// Check schema_version value
	if path == "" {
		if sv, ok := output["schema_version"]; ok {
			if sv != "1.0" {
				t.Errorf("schema_version = %v, want \"1.0\"", sv)
			}
		}
	}

	// Type-check each field present in output
	for key, val := range output {
		propSchema, ok := props[key]
		if !ok {
			continue // already reported above if additionalProperties:false
		}
		propMap, ok := propSchema.(map[string]any)
		if !ok {
			continue
		}
		fieldPath := key
		if path != "" {
			fieldPath = path + "." + key
		}
		validateType(t, fieldPath, val, resolveRef(propMap, root), root)
	}
}

// validateType checks that val matches the declared schema type.
func validateType(t *testing.T, path string, val any, schema map[string]any, root map[string]any) {
	t.Helper()
	schemaType := schema["type"]
	if schemaType == nil {
		return
	}

	switch st := schemaType.(type) {
	case string:
		checkSingleType(t, path, val, st, schema, root)
	case []any:
		// Nullable: e.g. ["string", "null"]
		if val == nil {
			// null is ok if "null" is in the type list
			for _, typ := range st {
				if typ == "null" {
					return
				}
			}
			t.Errorf("%s: got null, but type %v does not include null", path, st)
			return
		}
		ok := false
		for _, typ := range st {
			s, _ := typ.(string)
			if s == "null" {
				continue
			}
			if typMatches(val, s) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("%s: value %T does not match any of %v", path, val, st)
		}
	}
}

func checkSingleType(t *testing.T, path string, val any, typ string, schema map[string]any, root map[string]any) {
	t.Helper()
	if val == nil {
		t.Errorf("%s: got null for non-nullable type %q", path, typ)
		return
	}
	if !typMatches(val, typ) {
		t.Errorf("%s: expected type %q, got %T", path, typ, val)
		return
	}
	// Recurse into arrays
	if typ == "array" {
		arr, ok := val.([]any)
		if !ok {
			return
		}
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return
		}
		items = resolveRef(items, root)
		for i, elem := range arr {
			elemPath := fmt.Sprintf("%s[%d]", path, i)
			if itemType, _ := items["type"].(string); itemType == "object" {
				m, ok := elem.(map[string]any)
				if !ok {
					t.Errorf("%s: expected object, got %T", elemPath, elem)
					continue
				}
				validateObject(t, elemPath, m, items, root)
			} else {
				validateType(t, elemPath, elem, items, root)
			}
		}
	}
}

func typMatches(val any, typ string) bool {
	switch typ {
	case "string":
		_, ok := val.(string)
		return ok
	case "integer":
		f, ok := val.(float64)
		return ok && f == float64(int64(f))
	case "number":
		_, ok := val.(float64)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	}
	return false
}

func TestTraceGraphOutputSchema_RecursiveDefs(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(traceGraphOutputSchema), &schema); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("expected $defs in schema")
	}
	spanNode, ok := defs["span_node"].(map[string]any)
	if !ok {
		t.Fatal("expected span_node in $defs")
	}
	// Verify children self-references
	props := spanNode["properties"].(map[string]any)
	children := props["children"].(map[string]any)
	items := children["items"].(map[string]any)
	if ref, ok := items["$ref"]; !ok || ref != "#/$defs/span_node" {
		t.Errorf("children.items.$ref = %v, want #/$defs/span_node", ref)
	}
	// Verify roots uses $ref
	rootProps := schema["properties"].(map[string]any)
	roots := rootProps["roots"].(map[string]any)
	rootItems := roots["items"].(map[string]any)
	if ref, ok := rootItems["$ref"]; !ok || ref != "#/$defs/span_node" {
		t.Errorf("roots.items.$ref = %v, want #/$defs/span_node", ref)
	}
}
