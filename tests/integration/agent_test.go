package integration

import (
	"net/http"
	"testing"
)

func TestAgent_ToolNotFound(t *testing.T) {
	srv, _, _ := newIntegrationServer(t)

	w := httpPOST(t, srv.ToolCall, "/v1/tools/nonexistent_tool", map[string]string{})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
