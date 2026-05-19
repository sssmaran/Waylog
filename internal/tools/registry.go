package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ToolHandler func(ctx context.Context, store Store, params json.RawMessage) (any, error)

type Tool struct {
	Name         string
	Description  string
	Version      string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	Handler      ToolHandler
	Examples     []string
}

type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: map[string]Tool{},
	}
}

func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("tool name required")
	}
	if t.Handler == nil {
		return fmt.Errorf("tool handler required: %s", t.Name)
	}
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool already registered: %s", t.Name)
	}
	if len(t.OutputSchema) > 0 {
		inlined, err := inlineRefs(t.OutputSchema)
		if err != nil {
			return fmt.Errorf("inline refs for %s: %w", t.Name, err)
		}
		t.OutputSchema = inlined
	}
	r.tools[t.Name] = t
	return nil
}

// inlineRefs resolves $ref pointers within a JSON Schema by substituting
// definitions from $defs inline. Recursive references are replaced with
// {"type":"object"} to avoid infinite expansion. $defs is removed from
// the root after inlining.
func inlineRefs(schema json.RawMessage) (json.RawMessage, error) {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return schema, err
	}
	defs, hasDefs := root["$defs"].(map[string]any)
	if !hasDefs {
		return schema, nil
	}
	root = walkInline(root, defs, map[string]bool{}).(map[string]any)
	delete(root, "$defs")
	out, err := json.Marshal(root)
	if err != nil {
		return schema, err
	}
	return out, nil
}

// walkInline recursively replaces $ref values with their definitions.
// expanding tracks which def names are currently being expanded to detect cycles.
func walkInline(node any, defs map[string]any, expanding map[string]bool) any {
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			name := refName(ref)
			if name == "" {
				return v
			}
			if expanding[name] {
				return map[string]any{"type": "object"}
			}
			def, ok := defs[name]
			if !ok {
				return v
			}
			next := make(map[string]bool, len(expanding)+1)
			for k := range expanding {
				next[k] = true
			}
			next[name] = true
			return walkInline(def, defs, next)
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = walkInline(val, defs, expanding)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = walkInline(item, defs, expanding)
		}
		return out
	default:
		return v
	}
}

// refName extracts the definition name from a $ref value like "#/$defs/span_node".
func refName(ref string) string {
	const prefix = "#/$defs/"
	if strings.HasPrefix(ref, prefix) {
		return ref[len(prefix):]
	}
	return ""
}

// Replace inserts t under t.Name, overwriting any existing registration with
// the same name. Use this when intentionally swapping a v1 handler for a v2
// one (see explain_request / blast_radius v2 wiring in cmd/ingest/main.go).
// Returns an error only on invalid Tool definitions (empty Name, missing
// Handler, malformed schema). Does not error on overwrite — that is the
// entire point of this method.
func (r *Registry) Replace(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("tool name required")
	}
	if t.Handler == nil {
		return fmt.Errorf("tool handler required: %s", t.Name)
	}
	if len(t.OutputSchema) > 0 {
		inlined, err := inlineRefs(t.OutputSchema)
		if err != nil {
			return fmt.Errorf("inline refs for %s: %w", t.Name, err)
		}
		t.OutputSchema = inlined
	}
	r.tools[t.Name] = t
	return nil
}

func (r *Registry) Tool(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func (r *Registry) Call(ctx context.Context, store Store, name string, params json.RawMessage) (result any, err error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, &ToolError{Code: CodeNotFound, Message: fmt.Sprintf("unknown tool: %s", name), Retryable: false}
	}

	defer func() {
		if p := recover(); p != nil {
			err = &ToolError{Code: CodeInternal, Message: fmt.Sprintf("panic: %v", p), Retryable: true}
			result = nil
		}
	}()

	result, err = t.Handler(ctx, store, params)
	if err != nil {
		if _, ok := AsToolError(err); ok {
			return nil, err
		}
		return nil, &ToolError{Code: CodeInternal, Message: err.Error(), Retryable: false}
	}
	return result, nil
}
