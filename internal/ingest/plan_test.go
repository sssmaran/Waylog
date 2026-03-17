package ingest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// setupTestRegistry builds a registry with three mock tools for testing.
//
//	graph_insights  — requires window (string); outputs total_failures, schema_version
//	graph_failures  — optional limit (integer); outputs failures array with trace_id, error_code
//	explain_request — requires trace_id (string); outputs verdict
func setupTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()

	mustRegister := func(tool tools.Tool) {
		t.Helper()
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}

	mustRegister(tools.Tool{
		Name:        "graph_insights",
		Description: "Graph insights over a time window",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"window": {"type": "string"}
			},
			"required": ["window"],
			"additionalProperties": false
		}`),
		OutputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"total_failures": {"type": "integer"},
				"schema_version": {"type": "string"}
			}
		}`),
		Handler: func(_ context.Context, _ tools.Store, _ json.RawMessage) (any, error) {
			return map[string]any{"total_failures": 5, "schema_version": "1.0"}, nil
		},
	})

	mustRegister(tools.Tool{
		Name:        "graph_failures",
		Description: "List recent failures",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {"type": "integer"}
			},
			"additionalProperties": false
		}`),
		OutputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"failures": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"trace_id":   {"type": "string"},
							"error_code": {"type": "string"}
						}
					}
				}
			}
		}`),
		Handler: func(_ context.Context, _ tools.Store, _ json.RawMessage) (any, error) {
			return map[string]any{"failures": []any{}}, nil
		},
	})

	mustRegister(tools.Tool{
		Name:        "explain_request",
		Description: "Explain a request by trace ID",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"trace_id": {"type": "string"}
			},
			"required": ["trace_id"],
			"additionalProperties": false
		}`),
		OutputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"verdict": {"type": "string"}
			}
		}`),
		Handler: func(_ context.Context, _ tools.Store, _ json.RawMessage) (any, error) {
			return map[string]any{"verdict": "ok"}, nil
		},
	})

	return reg
}

func TestValidatePlan_Structural(t *testing.T) {
	reg := setupTestRegistry(t)

	t.Run("empty steps", func(t *testing.T) {
		errs := ValidatePlan([]PlanStep{}, reg)
		if len(errs) == 0 {
			t.Fatal("expected errors for empty steps, got none")
		}
	})

	t.Run("too many steps", func(t *testing.T) {
		steps := make([]PlanStep, 11)
		for i := range steps {
			steps[i] = PlanStep{ID: string(rune('a' + i)), Tool: "graph_insights", Params: json.RawMessage(`{"window":"10m"}`)}
		}
		errs := ValidatePlan(steps, reg)
		if len(errs) == 0 {
			t.Fatal("expected error for 11 steps, got none")
		}
	})

	t.Run("duplicate IDs", func(t *testing.T) {
		steps := []PlanStep{
			{ID: "a", Tool: "graph_insights", Params: json.RawMessage(`{"window":"10m"}`)},
			{ID: "a", Tool: "graph_failures", Params: json.RawMessage(`{}`)},
		}
		errs := ValidatePlan(steps, reg)
		if len(errs) == 0 {
			t.Fatal("expected error for duplicate step IDs, got none")
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		steps := []PlanStep{
			{ID: "x", Tool: "nonexistent_tool", Params: json.RawMessage(`{}`)},
		}
		errs := ValidatePlan(steps, reg)
		if len(errs) == 0 {
			t.Fatal("expected error for unknown tool, got none")
		}
	})

	t.Run("empty ID", func(t *testing.T) {
		steps := []PlanStep{
			{ID: "", Tool: "graph_insights", Params: json.RawMessage(`{"window":"10m"}`)},
		}
		errs := ValidatePlan(steps, reg)
		if len(errs) == 0 {
			t.Fatal("expected error for empty step ID, got none")
		}
	})
}

func TestValidatePlan_ForwardRef(t *testing.T) {
	reg := setupTestRegistry(t)

	// Step "a" refs step "b" which comes after it — invalid forward ref.
	steps := []PlanStep{
		{
			ID:     "a",
			Tool:   "explain_request",
			Params: json.RawMessage(`{"trace_id": "$steps[\"b\"].result.failures[0].trace_id"}`),
		},
		{
			ID:     "b",
			Tool:   "graph_failures",
			Params: json.RawMessage(`{}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) == 0 {
		t.Fatal("expected error for forward ref, got none")
	}
	t.Logf("forward ref errors: %v", errs)
}

func TestValidatePlan_SelfRef(t *testing.T) {
	reg := setupTestRegistry(t)

	// Step "a" refs itself — invalid self ref.
	steps := []PlanStep{
		{
			ID:     "a",
			Tool:   "explain_request",
			Params: json.RawMessage(`{"trace_id": "$steps[\"a\"].result.verdict"}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) == 0 {
		t.Fatal("expected error for self ref, got none")
	}
	t.Logf("self ref errors: %v", errs)
}

func TestValidatePlan_InvalidRefPath(t *testing.T) {
	reg := setupTestRegistry(t)

	// Step "b" refs step "a".nonexistent_field which doesn't exist in graph_insights output.
	steps := []PlanStep{
		{
			ID:     "a",
			Tool:   "graph_insights",
			Params: json.RawMessage(`{"window": "10m"}`),
		},
		{
			ID:     "b",
			Tool:   "explain_request",
			Params: json.RawMessage(`{"trace_id": "$steps[\"a\"].result.nonexistent_field"}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) == 0 {
		t.Fatal("expected error for invalid ref path, got none")
	}
	t.Logf("invalid ref path errors: %v", errs)
}

func TestValidatePlan_NonRefParamValidation(t *testing.T) {
	reg := setupTestRegistry(t)

	// graph_insights has additionalProperties: false.
	// "unknown_param" is not in its properties — should be rejected.
	steps := []PlanStep{
		{
			ID:     "a",
			Tool:   "graph_insights",
			Params: json.RawMessage(`{"window": "10m", "unknown_param": "bad"}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) == 0 {
		t.Fatal("expected error for unknown parameter, got none")
	}
	t.Logf("non-ref param errors: %v", errs)
}

func TestValidatePlan_RefFieldSkippedForInputValidation(t *testing.T) {
	reg := setupTestRegistry(t)

	// Step "b" uses a ref for trace_id (which is the only required field).
	// The ref field should be skipped during input schema validation —
	// so even though trace_id appears to be "missing" its literal value,
	// it should NOT produce an additionalProperties violation.
	steps := []PlanStep{
		{
			ID:     "a",
			Tool:   "graph_failures",
			Params: json.RawMessage(`{}`),
		},
		{
			ID:     "b",
			Tool:   "explain_request",
			Params: json.RawMessage(`{"trace_id": "$steps[\"a\"].result.failures[0].trace_id"}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) != 0 {
		t.Fatalf("expected no errors when ref field is used for required input, got: %v", errs)
	}
}

func TestValidatePlan_Valid(t *testing.T) {
	reg := setupTestRegistry(t)

	// 3-step chain: insights -> failures -> explain_request (with ref from failures)
	steps := []PlanStep{
		{
			ID:     "insights",
			Tool:   "graph_insights",
			Params: json.RawMessage(`{"window": "10m"}`),
		},
		{
			ID:     "failures",
			Tool:   "graph_failures",
			Params: json.RawMessage(`{"limit": 5}`),
		},
		{
			ID:     "explain",
			Tool:   "explain_request",
			Params: json.RawMessage(`{"trace_id": "$steps[\"failures\"].result.failures[0].trace_id"}`),
		},
	}
	errs := ValidatePlan(steps, reg)
	if len(errs) != 0 {
		t.Fatalf("expected valid plan to have no errors, got: %v", errs)
	}
}
