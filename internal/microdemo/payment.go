package microdemo

import (
	"encoding/json"
	"net/http"

	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	"github.com/sssmaran/WaylogCLI/pkg/waylog/trace"
)

type PaymentHandler struct{}

func NewPaymentHandler() *PaymentHandler {
	return &PaymentHandler{}
}

func (h *PaymentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     "demo-user",
		Tier:   "standard",
		Region: "us-east-1",
	})
	ctx = waylog.WithFlow(ctx, "payment")

	force := r.URL.Query().Get("force")
	success := true
	statusCode := http.StatusOK

	if force == "payment_fail" {
		success = false
		statusCode = http.StatusBadGateway
		waylog.Error(ctx, codedError{code: "PMT_502", message: "payment gateway failure"})
	}

	w.WriteHeader(statusCode)

	traceID := ""
	if tc, ok := trace.FromContext(ctx); ok {
		traceID = tc.TraceID
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success":  success,
		"trace_id": traceID,
		"amount":   99.99,
	})
}

type codedError struct {
	code    string
	message string
}

func (e codedError) Error() string { return e.message }
func (e codedError) Code() string  { return e.code }
