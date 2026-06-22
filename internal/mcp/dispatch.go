// Package mcp holds the transport-agnostic MCP (Model Context Protocol) core: the
// JSON-RPC wire types and the method dispatch over a tools.Registry. The stdio and
// HTTP transports are thin shells that parse a request, call Dispatch, and write
// the response — so protocol behavior (including the text/isError result shapes)
// lives in exactly one place.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// ProtocolVersion is the default MCP revision the server reports when a client
// requests a version we don't support. supportedVersions are the revisions we
// will echo back during initialize (negotiation): the original stdio/SSE
// revision and the Streamable-HTTP revision.
const ProtocolVersion = "2024-11-05"

var supportedVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
}

// ServerInfo identifies the server in the initialize handshake.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Request is a JSON-RPC 2.0 request (or notification when ID is absent).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
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
}

// toolsCallResult is an MCP CallToolResult. Content carries the universally
// consumable text form (the JSON-serialized tool output); StructuredContent
// carries the same payload as an object for capable clients; IsError marks a
// tool-level failure the model should read and reason about (vs. a transport error).
type toolsCallResult struct {
	Content           []toolContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

// Result builds a success Response.
func Result(id json.RawMessage, result any) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

// Errorf builds an error Response.
func Errorf(id json.RawMessage, code int, message string, data any) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: message, Data: data}}
}

// IsNotification reports whether a request is a JSON-RPC notification (no id),
// which must not be answered.
func IsNotification(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

// Dispatch handles one parsed JSON-RPC request against the tool registry and
// returns the response plus whether a response should be written (false for
// notifications). Transport-agnostic: callers handle framing and parse errors.
func Dispatch(ctx context.Context, reg *tools.Registry, info ServerInfo, req Request) (Response, bool) {
	if IsNotification(req.ID) {
		return Response{}, false
	}

	switch req.Method {
	case "initialize":
		// Version negotiation: echo the client's requested version when we
		// support it; otherwise return our default and let the client decide.
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := ProtocolVersion
		if supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return Result(req.ID, map[string]any{
			"protocolVersion": version,
			"serverInfo":      map[string]any{"name": info.Name, "version": info.Version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}), true

	case "tools/list":
		list := reg.List()
		out := make([]toolDescriptor, 0, len(list))
		for _, t := range list {
			out = append(out, toolDescriptor{
				Name:         t.Name,
				Description:  t.Description,
				InputSchema:  t.InputSchema,
				OutputSchema: t.OutputSchema,
			})
		}
		return Result(req.ID, toolsListResult{Tools: out}), true

	case "tools/call":
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Errorf(req.ID, -32602, "invalid params", err.Error()), true
		}
		if params.Name == "" {
			return Errorf(req.ID, -32602, "invalid params", "name required"), true
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage("{}")
		}
		result, err := reg.Call(ctx, params.Name, params.Arguments)
		if err != nil {
			// Tool execution failures are a result with isError (the model reads
			// and reacts to them), not a JSON-RPC transport error.
			return Result(req.ID, toolsCallResult{
				Content: []toolContent{{Type: "text", Text: err.Error()}},
				IsError: true,
			}), true
		}
		text, mErr := json.Marshal(result)
		if mErr != nil {
			return Errorf(req.ID, -32603, "internal error", mErr.Error()), true
		}
		return Result(req.ID, toolsCallResult{
			Content:           []toolContent{{Type: "text", Text: string(text)}},
			StructuredContent: result,
		}), true

	default:
		return Errorf(req.ID, -32601, "method not found", req.Method), true
	}
}

// nullID is the JSON-RPC id for responses to messages whose id we couldn't read
// (parse error / invalid request): the spec calls for "id": null, not omission.
var nullID = json.RawMessage("null")

// ParseError builds a JSON-RPC parse-error (-32700) response with id null.
func ParseError(data any) Response { return Errorf(nullID, -32700, "parse error", data) }

// HandleMessage parses one framed JSON-RPC message — a single request object or a
// batch array — dispatches each request, and returns the payload to write
// (a Response, or a []Response for a batch). write is false when there is nothing
// to send (a lone notification, or a batch of only notifications). A non-nil error
// means the body was not valid JSON; the transport turns that into a ParseError
// with its own status code. Shared by the stdio and HTTP transports.
func HandleMessage(ctx context.Context, reg *tools.Registry, info ServerInfo, raw []byte) (payload any, write bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, errors.New("empty message")
	}
	if trimmed[0] == '[' {
		var reqs []Request
		if uerr := json.Unmarshal(trimmed, &reqs); uerr != nil {
			return nil, false, uerr
		}
		if len(reqs) == 0 {
			return Errorf(nullID, -32600, "invalid request", "empty batch"), true, nil
		}
		resps := make([]Response, 0, len(reqs))
		for _, r := range reqs {
			if resp, has := Dispatch(ctx, reg, info, r); has {
				resps = append(resps, resp)
			}
		}
		if len(resps) == 0 {
			return nil, false, nil
		}
		return resps, true, nil
	}
	var req Request
	if uerr := json.Unmarshal(trimmed, &req); uerr != nil {
		return nil, false, uerr
	}
	resp, has := Dispatch(ctx, reg, info, req)
	if !has {
		return nil, false, nil
	}
	return resp, true, nil
}
