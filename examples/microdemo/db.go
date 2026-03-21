package microdemo

import (
	"encoding/json"
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

type DBHandler struct{}

func NewDBHandler() *DBHandler {
	return &DBHandler{}
}

func (h *DBHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     "demo-user",
		Tier:   "standard",
		Region: "us-east-1",
	})
	ctx = waylog.WithFlow(ctx, "db")

	traceID := ""
	if tc, ok := trace.FromContext(ctx); ok {
		traceID = tc.TraceID
	}

	if r.URL.Query().Get("force") == "db_fail" {
		waylog.Error(ctx, codedError{code: "DB_503", message: "database unavailable"})
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "database unavailable",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"trace_id": traceID,
		"result":   "db_ok",
	})
}
