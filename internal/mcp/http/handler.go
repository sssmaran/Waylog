// Package mcphttp is the Streamable-HTTP transport for the MCP server. A single
// POST /mcp endpoint accepts a JSON-RPC request, dispatches it through the shared
// internal/mcp core (same protocol behavior as the stdio transport), and returns
// the JSON-RPC response. It is stateless — tool calls are independent and
// deterministic, so no per-session state is tracked, which also lets any agent
// hit any replica of a shared server. Auth and rate limits are applied by the
// caller (the agent-scope middleware in cmd/ingest).
package mcphttp

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/sssmaran/WaylogCLI/internal/mcp"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// maxBody bounds the request body to protect the server from oversized payloads.
const maxBody = 1 << 20 // 1 MiB

// Handler returns an http.Handler serving MCP over Streamable HTTP at a single
// endpoint. Mount it (e.g. at /mcp) behind agent-scope auth.
func Handler(reg *tools.Registry, info mcp.ServerInfo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, mcp.ParseError("request too large"))
			return
		}

		payload, write, perr := mcp.HandleMessage(r.Context(), reg, info, body)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, mcp.ParseError(perr.Error()))
			return
		}
		if !write {
			// Notification(s) only: acknowledged, no body (Streamable HTTP).
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
