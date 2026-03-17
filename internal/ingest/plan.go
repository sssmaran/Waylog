package ingest

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// PlanStep describes a single step in an execution plan.
type PlanStep struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Params json.RawMessage `json:"params"`
}

// PlanStepError is a structured error for a plan step.
type PlanStepError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// PlanStepResult holds the outcome of a single executed step.
type PlanStepResult struct {
	ID         string         `json:"id"`
	Index      int            `json:"index"`
	Tool       string         `json:"tool"`
	Result     any            `json:"result,omitempty"`
	Error      *PlanStepError `json:"error"`
	DurationMs int64          `json:"duration_ms"`
}

// PlanResult holds the outcome of a completed plan execution.
type PlanResult struct {
	PlanID    string           `json:"plan_id"`
	Steps     []PlanStepResult `json:"steps"`
	Completed int              `json:"completed"`
	Total     int              `json:"total"`
	Status    string           `json:"status"`
	HaltedAt  *int             `json:"halted_at,omitempty"`
	Error     *PlanStepError   `json:"error,omitempty"`
}

// statusForHalt returns the appropriate status string for a halted plan.
func statusForHalt(haltedIdx int) string {
	if haltedIdx == 0 {
		return "failed"
	}
	return "partial"
}

// ValidatePlan performs two-phase validation of a plan's steps against the tool registry.
// Returns a list of error strings; empty means valid.
func ValidatePlan(steps []PlanStep, reg *tools.Registry) []string {
	// Phase 1 — Structural validation
	var errs []string

	if len(steps) == 0 {
		errs = append(errs, "plan must have at least 1 step")
		return errs
	}
	if len(steps) > 10 {
		errs = append(errs, fmt.Sprintf("plan has %d steps; maximum is 10", len(steps)))
		return errs
	}

	seenIDs := make(map[string]bool, len(steps))
	for i, step := range steps {
		if step.ID == "" {
			errs = append(errs, fmt.Sprintf("step %d: id is required", i))
		}
		if step.Tool == "" {
			errs = append(errs, fmt.Sprintf("step %d: tool is required", i))
		} else if _, ok := reg.Tool(step.Tool); !ok {
			errs = append(errs, fmt.Sprintf("step %q: unknown tool %q", step.ID, step.Tool))
		}
		if step.ID != "" {
			if seenIDs[step.ID] {
				errs = append(errs, fmt.Sprintf("duplicate step id %q", step.ID))
			}
			seenIDs[step.ID] = true
		}
	}

	if len(errs) > 0 {
		return errs
	}

	// Build ordered index: step id -> position
	idxByID := make(map[string]int, len(steps))
	for i, step := range steps {
		idxByID[step.ID] = i
	}

	// Phase 2 — Interpolation-aware validation
	for i, step := range steps {
		tool, _ := reg.Tool(step.Tool)

		params := step.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}

		refs, err := FindRefs(params)
		if err != nil {
			errs = append(errs, fmt.Sprintf("step %q: %s", step.ID, err.Error()))
			continue
		}

		// Collect JSON pointer positions that contain refs (for skipping input validation)
		refPointers := make(map[string]bool, len(refs))
		for _, fr := range refs {
			refPointers[fr.Pointer] = true

			refIdx, exists := idxByID[fr.Ref.StepID]
			if !exists {
				errs = append(errs, fmt.Sprintf("step %q: ref to unknown step id %q", step.ID, fr.Ref.StepID))
				continue
			}
			if refIdx >= i {
				errs = append(errs, fmt.Sprintf("step %q: ref to step %q is a forward or self-reference (step index %d >= %d)", step.ID, fr.Ref.StepID, refIdx, i))
				continue
			}

			// Validate ref path against referenced tool's output schema
			refTool, _ := reg.Tool(steps[refIdx].Tool)
			if len(refTool.OutputSchema) > 0 {
				if pathErr := ValidateRefPath(fr.Ref, refTool.OutputSchema); pathErr != nil {
					errs = append(errs, fmt.Sprintf("step %q: %s", step.ID, pathErr.Error()))
				}
			}
		}

		// Non-ref params: validate against input schema for additionalProperties
		if len(tool.InputSchema) > 0 {
			if paramErr := validateNonRefParams(params, refPointers, tool.InputSchema); paramErr != nil {
				errs = append(errs, fmt.Sprintf("step %q: %s", step.ID, paramErr.Error()))
			}
		}
	}

	return errs
}

// validateNonRefParams strips ref-containing fields from params and validates
// the remaining fields against the tool's input schema.
func validateNonRefParams(params json.RawMessage, refPointers map[string]bool, inputSchema json.RawMessage) error {
	if len(refPointers) == 0 {
		return validateParamsAgainstSchema(params, inputSchema)
	}

	// Parse params and strip top-level fields that are refs.
	// refPointers use JSON Pointer notation like "/field_name".
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		// Not an object, nothing to validate.
		return nil
	}

	// Collect top-level ref field names (pointer "/field" -> "field")
	refFields := make(map[string]bool, len(refPointers))
	for ptr := range refPointers {
		if strings.HasPrefix(ptr, "/") {
			field := strings.SplitN(ptr[1:], "/", 2)[0]
			refFields[jsonPointerUnescape(field)] = true
		}
	}

	// Build stripped params without ref fields.
	stripped := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		if !refFields[k] {
			stripped[k] = v
		}
	}

	strippedJSON, err := json.Marshal(stripped)
	if err != nil {
		return err
	}
	return validateParamsAgainstSchema(strippedJSON, inputSchema)
}

// validateParamsAgainstSchema performs a lightweight check: if the schema has
// additionalProperties: false, reject any param keys not in the schema's properties.
func validateParamsAgainstSchema(params json.RawMessage, schema json.RawMessage) error {
	var schemaObj map[string]any
	if err := json.Unmarshal(schema, &schemaObj); err != nil {
		return nil
	}

	addlProps, hasAddl := schemaObj["additionalProperties"]
	if !hasAddl {
		return nil
	}
	addlBool, isBool := addlProps.(bool)
	if !isBool || addlBool {
		return nil
	}

	// additionalProperties: false — check that all param keys are known
	props, _ := schemaObj["properties"].(map[string]any)

	var paramObj map[string]json.RawMessage
	if err := json.Unmarshal(params, &paramObj); err != nil {
		return nil
	}

	for k := range paramObj {
		if _, known := props[k]; !known {
			return fmt.Errorf("unknown parameter %q (additionalProperties is false)", k)
		}
	}
	return nil
}
