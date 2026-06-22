package stdio

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/mcp"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// drive feeds newline-delimited JSON-RPC requests through Serve and returns the
// decoded responses (one per line of output).
func drive(t *testing.T, reg *tools.Registry, lines ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out, reg, ServerInfo{Name: "waylog", Version: "test"}); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("decode response %q: %v", l, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func testRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "echo",
		Description: "echoes its args",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.Tool{
		Name:        "boom",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, &tools.ToolError{Code: tools.CodeNotFound, Message: "nothing here"}
		},
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestServeInitializeAndList(t *testing.T) {
	resps := drive(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	// The notification must NOT produce a response: 2 requests -> 2 responses.
	if len(resps) != 2 {
		t.Fatalf("want 2 responses (notification is silent), got %d: %v", len(resps), resps)
	}
	initRes := resps[0]["result"].(map[string]any)
	if initRes["protocolVersion"] != mcp.ProtocolVersion {
		t.Fatalf("protocolVersion = %v", initRes["protocolVersion"])
	}
	if _, ok := initRes["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("initialize must advertise tools capability: %v", initRes)
	}
	list := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(list) != 2 {
		t.Fatalf("tools/list len = %d, want 2", len(list))
	}
	// inputSchema (camelCase) must be present for client self-discovery.
	if _, ok := list[0].(map[string]any)["inputSchema"]; !ok {
		t.Fatalf("tool missing inputSchema: %v", list[0])
	}
}

func TestServeToolCallReturnsTextAndStructured(t *testing.T) {
	resps := drive(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
	)
	res := resps[0]["result"].(map[string]any)
	content := res["content"].([]any)
	first := content[0].(map[string]any)
	// MUST be a standard "text" content type (not "json"), or clients can't read it.
	if first["type"] != "text" {
		t.Fatalf("content type = %v, want text", first["type"])
	}
	if !strings.Contains(first["text"].(string), `"ok":true`) {
		t.Fatalf("text payload missing tool output: %v", first["text"])
	}
	if _, ok := res["structuredContent"]; !ok {
		t.Fatalf("structuredContent missing for capable clients: %v", res)
	}
	if res["isError"] == true {
		t.Fatalf("success call must not be isError")
	}
}

func TestServeToolErrorIsResultNotTransportError(t *testing.T) {
	resps := drive(t, testRegistry(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`,
	)
	// A tool failure is a result with isError, NOT a JSON-RPC error, so the
	// model can read and reason about it.
	if _, isErr := resps[0]["error"]; isErr {
		t.Fatalf("tool failure must not be a JSON-RPC error: %v", resps[0])
	}
	res := resps[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError=true, got %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "nothing here") {
		t.Fatalf("error text not surfaced to model: %v", text)
	}
}

// errorCode extracts the JSON-RPC error code from a decoded response (JSON
// numbers decode to float64).
func errorCode(t *testing.T, resp map[string]any) int {
	t.Helper()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	return int(e["code"].(float64))
}

func TestServeParseErrorIsRPCError(t *testing.T) {
	resps := drive(t, testRegistry(t), `{not valid json`)
	if got := errorCode(t, resps[0]); got != -32700 {
		t.Fatalf("malformed input code = %d, want -32700 (parse error)", got)
	}
}

func TestServeUnknownMethodIsRPCError(t *testing.T) {
	resps := drive(t, testRegistry(t), `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`)
	if got := errorCode(t, resps[0]); got != -32601 {
		t.Fatalf("unknown method code = %d, want -32601 (method not found)", got)
	}
}

func TestServeToolsCallMissingNameIsRPCError(t *testing.T) {
	resps := drive(t, testRegistry(t), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":""}}`)
	if got := errorCode(t, resps[0]); got != -32602 {
		t.Fatalf("missing tool name code = %d, want -32602 (invalid params)", got)
	}
}
