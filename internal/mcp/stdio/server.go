// Package stdio is the stdio transport for the MCP server: it frames JSON-RPC
// messages as newline-delimited JSON on stdin/stdout and delegates all protocol
// handling to internal/mcp (shared with the HTTP transport).
package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/mcp"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

// ServerInfo identifies the server in the initialize handshake.
type ServerInfo = mcp.ServerInfo

// Serve runs the MCP stdio loop: read one JSON-RPC message per line, dispatch it
// against the registry, and write the response (notifications get none).
func Serve(ctx context.Context, in io.Reader, out io.Writer, reg *tools.Registry, info ServerInfo) error {
	if reg == nil {
		return fmt.Errorf("registry required")
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
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		payload, write, err := mcp.HandleMessage(ctx, reg, info, []byte(line))
		if err != nil {
			_ = enc.Encode(mcp.ParseError(err.Error()))
			continue
		}
		if write {
			_ = enc.Encode(payload)
		}
	}
}
