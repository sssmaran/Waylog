package tools

import (
	"encoding/json"
	"testing"
)

func TestInlineRefs_Simple(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"$defs": {
			"addr": {
				"type": "object",
				"properties": {
					"street": { "type": "string" },
					"city":   { "type": "string" }
				}
			}
		},
		"properties": {
			"name":    { "type": "string" },
			"address": { "$ref": "#/$defs/addr" }
		}
	}`)

	out, err := inlineRefs(schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}

	// $defs must be removed
	if _, hasDefs := result["$defs"]; hasDefs {
		t.Error("$defs should have been removed from result")
	}

	// address property should be inlined
	props, _ := result["properties"].(map[string]any)
	addrProp, ok := props["address"].(map[string]any)
	if !ok {
		t.Fatal("address property missing or wrong type")
	}
	if addrProp["type"] != "object" {
		t.Errorf("inlined address should have type=object, got %v", addrProp["type"])
	}
	innerProps, ok := addrProp["properties"].(map[string]any)
	if !ok {
		t.Fatal("inlined address should have properties")
	}
	if _, ok := innerProps["street"]; !ok {
		t.Error("inlined address missing 'street' property")
	}
	if _, ok := innerProps["city"]; !ok {
		t.Error("inlined address missing 'city' property")
	}
}

func TestInlineRefs_Recursive(t *testing.T) {
	// span_node references itself via children — mirrors traceGraphOutputSchema
	schema := json.RawMessage(`{
		"type": "object",
		"$defs": {
			"span_node": {
				"type": "object",
				"properties": {
					"span_id":  { "type": "string" },
					"service":  { "type": ["string", "null"] },
					"children": { "type": "array", "items": { "$ref": "#/$defs/span_node" } }
				},
				"additionalProperties": false
			}
		},
		"properties": {
			"schema_version": { "type": "string" },
			"trace_id":       { "type": "string" },
			"roots":          { "type": "array", "items": { "$ref": "#/$defs/span_node" } }
		},
		"required": ["schema_version", "trace_id", "roots"],
		"additionalProperties": false
	}`)

	out, err := inlineRefs(schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}

	// $defs must be removed
	if _, hasDefs := result["$defs"]; hasDefs {
		t.Error("$defs should have been removed from result")
	}

	// roots should be inlined with span_node definition
	props, _ := result["properties"].(map[string]any)
	rootsProp, ok := props["roots"].(map[string]any)
	if !ok {
		t.Fatal("roots property missing or wrong type")
	}
	items, ok := rootsProp["items"].(map[string]any)
	if !ok {
		t.Fatal("roots.items should be an object after inlining")
	}
	if items["type"] != "object" {
		t.Errorf("roots.items should have type=object, got %v", items["type"])
	}

	// children inside the inlined span_node should be opaque {"type":"object"}
	// because span_node is recursive
	innerProps, _ := items["properties"].(map[string]any)
	children, ok := innerProps["children"].(map[string]any)
	if !ok {
		t.Fatal("children property missing in inlined span_node")
	}
	childItems, ok := children["items"].(map[string]any)
	if !ok {
		t.Fatal("children.items should be an object (opaque fallback)")
	}
	if childItems["type"] != "object" {
		t.Errorf("recursive children.items should be opaque {type:object}, got %v", childItems["type"])
	}
	// The opaque fallback must not have sub-properties (it was truncated)
	if _, hasProps := childItems["properties"]; hasProps {
		t.Error("recursive ref should be opaque {type:object} without properties")
	}
}

func TestInlineRefs_NoDefs(t *testing.T) {
	original := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": { "type": "string" },
			"age":  { "type": "integer" }
		},
		"required": ["name"]
	}`)

	out, err := inlineRefs(original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var orig, result map[string]any
	_ = json.Unmarshal(original, &orig)
	_ = json.Unmarshal(out, &result)

	// type should be preserved
	if result["type"] != "object" {
		t.Errorf("type should be object, got %v", result["type"])
	}

	props, _ := result["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Error("name property should be present")
	}
	if _, ok := props["age"]; !ok {
		t.Error("age property should be present")
	}
}
