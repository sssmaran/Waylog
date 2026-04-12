package microdemo

import (
	"encoding/json"
	"io"
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

type CheckoutHandler struct {
	paymentURL    string
	dbURL         string
	paymentClient *http.Client
	dbClient      *http.Client
}

func NewCheckoutHandler(paymentURL, dbURL string) *CheckoutHandler {
	return &CheckoutHandler{
		paymentURL: paymentURL,
		dbURL:      dbURL,
		paymentClient: &http.Client{
			Transport: wayloghttp.WrapTransport(http.DefaultTransport, "payment"),
		},
		dbClient: &http.Client{
			Transport: wayloghttp.WrapTransport(http.DefaultTransport, "db"),
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

	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     "demo-user",
		Tier:   "standard",
		Region: "us-east-1",
	})
	ctx = waylog.WithFlow(ctx, "checkout")

	if force == "checkout_fail" {
		waylog.Error(ctx, codedError{code: "CHK_500", message: "checkout processing failed"})

		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "checkout processing failed",
		})
		return
	}

	dbReq, err := http.NewRequestWithContext(ctx, "GET", h.dbURL+"/db?force="+force, nil)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    err.Error(),
		})
		return
	}

	dbResp, err := h.dbClient.Do(dbReq)
	if err != nil {
		waylog.Error(ctx, codedError{code: "CHK_DB_502", message: "db service unavailable"})
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "db service unavailable",
		})
		return
	}
	_, _ = io.Copy(io.Discard, dbResp.Body)
	_ = dbResp.Body.Close()

	if dbResp.StatusCode >= http.StatusInternalServerError {
		waylog.Error(ctx, codedError{code: "CHK_DB_DOWNSTREAM", message: "downstream db failed"})
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
			"error":    "database operation failed",
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

	resp, err := h.paymentClient.Do(req)
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

	if resp.StatusCode >= http.StatusInternalServerError {
		waylog.Error(ctx, codedError{code: "CHK_DOWNSTREAM", message: "downstream payment failed"})
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
