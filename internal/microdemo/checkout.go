package microdemo

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	"github.com/sssmaran/WaylogCLI/pkg/waylog/trace"
)

type CheckoutHandler struct {
	paymentURL string
	client     *http.Client
}

func NewCheckoutHandler(paymentURL string) *CheckoutHandler {
	return &CheckoutHandler{
		paymentURL: paymentURL,
		client: &http.Client{
			Transport: wayloghttp.WrapTransport(http.DefaultTransport, "payment-demo"),
		},
	}
}

func (h *CheckoutHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	force := r.URL.Query().Get("force")

	traceID := ""
	if tc, ok := trace.FromContext(ctx); ok {
		traceID = tc.TraceID
	}

	if force == "checkout_fail" {
		ctx = waylog.WithUser(ctx, waylog.User{
			ID:     "demo-user",
			Tier:   "standard",
			Region: "us-east-1",
		})
		ctx = waylog.WithFlow(ctx, "checkout")
		waylog.Error(ctx, codedError{code: "CHK_500", message: "checkout processing failed"})

		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "checkout processing failed",
		})
		return
	}

	req, err := http.NewRequestWithContext(ctx, "GET", h.paymentURL+"/pay?force="+force, nil)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    err.Error(),
		})
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		waylog.Error(ctx, codedError{code: "CHK_502", message: "payment service unavailable"})
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "payment service unavailable",
		})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     "demo-user",
		Tier:   "standard",
		Region: "us-east-1",
	})
	ctx = waylog.WithFlow(ctx, "checkout")

	if resp.StatusCode >= http.StatusInternalServerError {
		waylog.Error(ctx, codedError{code: "CHK_DOWNSTREAM", message: "downstream payment failed"})
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
