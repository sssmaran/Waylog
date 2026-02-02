package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/tools"
)

const protocolVersion = "2024-11-05"

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolDescriptor struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	JSON any    `json:"json,omitempty"`
}

type toolsCallResult struct {
	Content []toolContent `json:"content"`
}

func Serve(ctx context.Context, in io.Reader, out io.Writer, reg *tools.Registry, store tools.Store, info ServerInfo) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if store == nil {
		return fmt.Errorf("store required")
	}

	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeError(enc, nil, -32700, "parse error", err.Error())
			continue
		}

		if isNotification(req.ID) {
			handleNotification(ctx, req, reg, store, info)
			continue
		}

		switch req.Method {
		case "initialize":
			result := map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo": map[string]any{
					"name":    info.Name,
					"version": info.Version,
				},
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
			}
			writeResult(enc, req.ID, result)
		case "tools/list":
			list := reg.List()
			toolsOut := make([]toolDescriptor, 0, len(list))
			for _, t := range list {
				toolsOut = append(toolsOut, toolDescriptor{
					Name:         t.Name,
					Description:  t.Description,
					InputSchema:  t.InputSchema,
					OutputSchema: t.OutputSchema,
				})
			}
			writeResult(enc, req.ID, toolsListResult{Tools: toolsOut})
		case "tools/call":
			var params toolsCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(enc, req.ID, -32602, "invalid params", err.Error())
				continue
			}
			if params.Name == "" {
				writeError(enc, req.ID, -32602, "invalid params", "name required")
				continue
			}
			if len(params.Arguments) == 0 {
				params.Arguments = json.RawMessage("{}")
			}
			result, err := reg.Call(ctx, store, params.Name, params.Arguments)
			if err != nil {
				writeError(enc, req.ID, -32000, "tool error", err.Error())
				continue
			}
			writeResult(enc, req.ID, toolsCallResult{
				Content: []toolContent{
					{
						Type: "json",
						JSON: result,
					},
				},
			})
		default:
			writeError(enc, req.ID, -32601, "method not found", req.Method)
		}
	}
}

func handleNotification(ctx context.Context, req rpcRequest, reg *tools.Registry, store tools.Store, info ServerInfo) {
	_ = ctx
	_ = reg
	_ = store
	_ = info
}

func writeResult(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeError(enc *json.Encoder, id json.RawMessage, code int, message string, data any) {
	_ = enc.Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

func isNotification(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	if string(id) == "null" {
		return true
	}
	return false
}
