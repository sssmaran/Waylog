package mcphttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/mcp"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.Tool{
		Name:        "echo",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler:     func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	return Handler(reg, mcp.ServerInfo{Name: "waylog", Version: "test"})
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestHTTPInitialize(t *testing.T) {
	w := post(t, testHandler(t), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var resp mcp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.(map[string]any)["protocolVersion"] != mcp.ProtocolVersion {
		t.Fatalf("protocolVersion wrong: %v", resp.Result)
	}
}

func TestHTTPToolCall(t *testing.T) {
	w := post(t, testHandler(t), `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := resp["result"].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "text" {
		t.Fatalf("content type wrong: %v", content[0])
	}
}

func TestHTTPBatchReturnsArray(t *testing.T) {
	w := post(t, testHandler(t),
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}},`+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("batch status = %d, want 200", w.Code)
	}
	var arr []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
		t.Fatalf("batch response must be a JSON array: %v (%s)", err, w.Body.String())
	}
	if len(arr) != 2 {
		t.Fatalf("want 2 responses in the batch, got %d", len(arr))
	}
}

func TestHTTPNotificationReturns202(t *testing.T) {
	w := post(t, testHandler(t), `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "" {
		t.Fatalf("notification must have empty body, got %q", w.Body.String())
	}
}

func TestHTTPMalformedJSONIs400WithRPCError(t *testing.T) {
	w := post(t, testHandler(t), `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp mcp.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Fatalf("want JSON-RPC parse error -32700, got %+v", resp)
	}
}

func TestHTTPRejectsNonPOST(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	testHandler(t).ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", w.Code)
	}
}
