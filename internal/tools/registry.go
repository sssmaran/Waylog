package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

type ToolHandler func(ctx context.Context, store Store, params json.RawMessage) (any, error)

type Tool struct {
	Name         string
	Description  string
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

func (r *Registry) Call(ctx context.Context, store Store, name string, params json.RawMessage) (any, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return t.Handler(ctx, store, params)
}
