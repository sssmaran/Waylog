package microdemo

import (
	"encoding/json"
	"net/http"
	"strconv"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
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
	ctx = waylog.WithMetadataKey(ctx, "tenant_id", "acme-corp")
	ctx = waylog.WithMetadataKey(ctx, "cart_total_cents", 9999)

	force := r.URL.Query().Get("force")
	success := true
	statusCode := http.StatusOK
	attempt := 1

	switch force {
	case "payment_fail":
		success = false
		statusCode = http.StatusBadGateway
		ctx = waylog.WithErrorReason(ctx, "upstream acquirer returned 502; no retry configured for this route")
		ctx = waylog.WithErrorPath(ctx, "https://runbooks.example.com/payments-502")
		waylog.Error(ctx, codedError{code: "PMT_502", message: "payment gateway failure"})
	case "payment_retry":
		if n, err := strconv.Atoi(r.URL.Query().Get("attempt")); err == nil && n > 0 {
			attempt = n
		}
		ctx = waylog.WithAttempt(ctx, attempt)
		if attempt > 1 {
			ctx = waylog.WithRetry(ctx, waylog.Retry{Of: 3, PreviousAttemptID: r.URL.Query().Get("prev_attempt_id")})
		}
		if attempt < 3 {
			success = false
			statusCode = http.StatusBadGateway
			ctx = waylog.WithErrorReason(ctx, "upstream acquirer returned 502; retry policy permits up to 3 attempts")
			ctx = waylog.WithErrorPath(ctx, "https://runbooks.example.com/payments-502")
			waylog.Error(ctx, codedError{code: "PMT_502", message: "payment gateway failure"})
		}
	}

	w.WriteHeader(statusCode)

	traceID, spanID := "", ""
	if tc, ok := trace.FromContext(ctx); ok {
		traceID = tc.TraceID
		spanID = tc.SpanID
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success":  success,
		"trace_id": traceID,
		"span_id":  spanID,
		"attempt":  attempt,
		"amount":   99.99,
	})
}

type codedError struct {
	code    string
	message string
}

func (e codedError) Error() string { return e.message }
func (e codedError) Code() string  { return e.code }
