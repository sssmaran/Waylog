package checkout

import (
	"encoding/json"
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := randomUser()

	result := h.svc.Process(CheckoutRequest{User: user})

	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     user.ID,
		Tier:   user.Tier,
		Region: user.Region,
		VIP:    user.VIP,
	})
	ctx = waylog.WithFlow(ctx, result.Flow)
	ctx = waylog.WithFlags(ctx, result.Flags)

	if !result.Success {
		waylog.Error(ctx, codedError{code: result.ErrorCode, message: result.ErrorMsg})
	}

	w.WriteHeader(result.StatusCode)
	traceID := ""
	if tc, ok := trace.FromContext(ctx); ok {
		traceID = tc.TraceID
	}
	json.NewEncoder(w).Encode(map[string]any{
		"success":  result.Success,
		"trace_id": traceID,
	})
}

type codedError struct {
	code    string
	message string
}

func (e codedError) Error() string {
	return e.message
}

func (e codedError) Code() string {
	return e.code
}
