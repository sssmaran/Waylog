package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func testRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "echo",
		Description: "echoes ok",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler:     func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil },
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

func dispatch(t *testing.T, reg *tools.Registry, body string) (Response, bool) {
	t.Helper()
	var r Request
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	return Dispatch(context.Background(), reg, ServerInfo{Name: "waylog", Version: "test"}, r)
}

func TestDispatchInitialize(t *testing.T) {
	resp, has := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if !has {
		t.Fatal("initialize must produce a response")
	}
	res := resp.Result.(map[string]any)
	if res["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("must advertise tools capability: %v", res)
	}
}

func TestDispatchInitializeNegotiatesVersion(t *testing.T) {
	reg := testRegistry(t)
	// Supported requested version is echoed back.
	resp, _ := dispatch(t, reg, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if got := resp.Result.(map[string]any)["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("supported version not echoed: got %v", got)
	}
	// Unsupported requested version falls back to the server default.
	resp, _ = dispatch(t, reg, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if got := resp.Result.(map[string]any)["protocolVersion"]; got != ProtocolVersion {
		t.Fatalf("unsupported version should fall back to %s, got %v", ProtocolVersion, got)
	}
}

func TestHandleMessageBatch(t *testing.T) {
	reg := testRegistry(t)
	// Batch of [echo call, notification, boom call] → 2 responses (notification drops).
	payload, write, err := HandleMessage(context.Background(), reg, ServerInfo{},
		[]byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}},`+
			`{"jsonrpc":"2.0","method":"notifications/x"},`+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"boom","arguments":{}}}]`))
	if err != nil || !write {
		t.Fatalf("batch: write=%v err=%v", write, err)
	}
	resps := payload.([]Response)
	if len(resps) != 2 {
		t.Fatalf("batch should drop the notification → 2 responses, got %d", len(resps))
	}
}

func TestHandleMessageBatchAllNotificationsIsSilent(t *testing.T) {
	_, write, err := HandleMessage(context.Background(), testRegistry(t), ServerInfo{},
		[]byte(`[{"jsonrpc":"2.0","method":"a"},{"jsonrpc":"2.0","method":"b"}]`))
	if err != nil || write {
		t.Fatalf("all-notification batch must be silent: write=%v err=%v", write, err)
	}
}

func TestHandleMessageMalformedReturnsError(t *testing.T) {
	if _, _, err := HandleMessage(context.Background(), testRegistry(t), ServerInfo{}, []byte(`{not json`)); err == nil {
		t.Fatal("malformed JSON must return an error for the transport to map to a parse error")
	}
}

func TestParseErrorHasNullID(t *testing.T) {
	raw, err := json.Marshal(ParseError("boom"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"id":null`) {
		t.Fatalf("parse error must carry id:null per JSON-RPC, got %s", raw)
	}
}

func TestDispatchNotificationIsSilent(t *testing.T) {
	if _, has := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","method":"notifications/initialized"}`); has {
		t.Fatal("a notification (no id) must NOT produce a response")
	}
}

func TestDispatchToolsList(t *testing.T) {
	resp, _ := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := resp.Result.(toolsListResult).Tools
	if len(list) != 2 {
		t.Fatalf("tools/list len = %d, want 2", len(list))
	}
	if list[0].InputSchema == nil {
		t.Fatalf("tool missing inputSchema: %+v", list[0])
	}
}

func TestDispatchToolsCallText(t *testing.T) {
	resp, _ := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	res := resp.Result.(toolsCallResult)
	if res.IsError {
		t.Fatal("success call must not be isError")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || !strings.Contains(res.Content[0].Text, `"ok":true`) {
		t.Fatalf("want one text block with tool output, got %+v", res.Content)
	}
	if res.StructuredContent == nil {
		t.Fatal("structuredContent must be set for capable clients")
	}
}

func TestDispatchToolErrorIsResultNotRPCError(t *testing.T) {
	resp, _ := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	if resp.Error != nil {
		t.Fatalf("tool failure must be a result with isError, not a JSON-RPC error: %+v", resp.Error)
	}
	res := resp.Result.(toolsCallResult)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "nothing here") {
		t.Fatalf("expected isError result surfacing the message, got %+v", res)
	}
}

func TestDispatchUnknownMethod(t *testing.T) {
	resp, has := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":5,"method":"does/not/exist"}`)
	if !has || resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("unknown method must return -32601, got %+v", resp)
	}
}

func TestDispatchToolsCallBadParams(t *testing.T) {
	resp, _ := dispatch(t, testRegistry(t), `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":""}}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("missing tool name must return -32602, got %+v", resp)
	}
}
