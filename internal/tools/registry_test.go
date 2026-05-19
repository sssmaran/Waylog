package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type nullStore struct{}

func (nullStore) Snapshot() interface{ Nodes() int } { return nil }

func TestCall_PanicRecovery(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{
		Name: "panicker",
		Handler: func(ctx context.Context, store Store, params json.RawMessage) (any, error) {
			panic("kaboom")
		},
	})

	_, err := reg.Call(context.Background(), nil, "panicker", nil)
	te, ok := AsToolError(err)
	if !ok {
		t.Fatalf("expected ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeInternal {
		t.Errorf("code = %q, want %q", te.Code, CodeInternal)
	}
	if !te.Retryable {
		t.Error("panic recovery should be retryable")
	}
}

func TestCall_RawErrorWrapping(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{
		Name: "raw",
		Handler: func(ctx context.Context, store Store, params json.RawMessage) (any, error) {
			return nil, errors.New("plain error")
		},
	})

	_, err := reg.Call(context.Background(), nil, "raw", nil)
	te, ok := AsToolError(err)
	if !ok {
		t.Fatalf("expected ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeInternal {
		t.Errorf("code = %q, want %q", te.Code, CodeInternal)
	}
}

func TestCall_ToolErrorPassthrough(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{
		Name: "typed",
		Handler: func(ctx context.Context, store Store, params json.RawMessage) (any, error) {
			return nil, &ToolError{Code: CodeInvalidParams, Message: "bad"}
		},
	})

	_, err := reg.Call(context.Background(), nil, "typed", nil)
	te, ok := AsToolError(err)
	if !ok {
		t.Fatalf("expected ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeInvalidParams {
		t.Errorf("code = %q, want %q", te.Code, CodeInvalidParams)
	}
}

func TestCall_UnknownTool(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Call(context.Background(), nil, "nope", nil)
	te, ok := AsToolError(err)
	if !ok {
		t.Fatalf("expected ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", te.Code, CodeNotFound)
	}
}

func TestCall_Success(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{
		Name: "ok",
		Handler: func(ctx context.Context, store Store, params json.RawMessage) (any, error) {
			return map[string]int{"x": 1}, nil
		},
	})

	result, err := reg.Call(context.Background(), nil, "ok", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]int)
	if !ok || m["x"] != 1 {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestRegistry_Replace_Overwrites(t *testing.T) {
	reg := NewRegistry()
	h := func(_ context.Context, _ Store, _ json.RawMessage) (any, error) { return nil, nil }
	if err := reg.Register(Tool{Name: "x", Handler: h, Description: "v1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Replace(Tool{Name: "x", Handler: h, Description: "v2"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, ok := reg.Tool("x")
	if !ok {
		t.Fatal("tool x missing after Replace")
	}
	if got.Description != "v2" {
		t.Errorf("Description = %q; want v2", got.Description)
	}
}

func TestRegistry_Replace_RejectsInvalidTool(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Replace(Tool{Name: ""}); err == nil {
		t.Error("expected error on empty name")
	}
	if err := reg.Replace(Tool{Name: "x"}); err == nil {
		t.Error("expected error on nil handler")
	}
}
