package microdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
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
			Transport: wayloghttp.NewTransport(http.DefaultTransport, "payment"),
		},
		dbClient: &http.Client{
			Transport: wayloghttp.NewTransport(http.DefaultTransport, "db"),
		},
	}
}

func (h *CheckoutHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqBody, err := parsePurchaseRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	setDemoFields(ctx, "checkout", reqBody)

	if reqBody.Scenario == ScenarioSuppressedPayment502 {
		h.serveSuppressedPayment(w, reqBody, ctx)
		return
	}

	if err := h.loadCart(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "database operation failed"))
		return
	}
	if err := h.chargePayment(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "payment gateway failure"))
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response(ctx, true, reqBody, ""))
}

func (h *CheckoutHandler) serveSuppressedPayment(w http.ResponseWriter, reqBody PurchaseRequest, ctx context.Context) {
	_ = h.loadCart(ctx, reqBody)
	_, _ = h.callPayment(ctx, reqBody)
	w.WriteHeader(http.StatusBadGateway)
	waylogv2.Suppress(ctx)
	_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "known payment gateway issue suppressed"))
}

func (h *CheckoutHandler) loadCart(ctx context.Context, reqBody PurchaseRequest) error {
	return waylogv2.StepVoid(ctx, "db.load_cart", func(ctx context.Context) error {
		raw, _ := json.Marshal(reqBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dbURL+"/db", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.dbClient.Do(req)
		if err != nil {
			return waylogv2.NewError("DB_503", waylogv2.WithReason("db service unavailable"))
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= http.StatusInternalServerError {
			return waylogv2.NewError("DB_503", waylogv2.WithReason("database unavailable"))
		}
		return nil
	})
}

func (h *CheckoutHandler) chargePayment(ctx context.Context, reqBody PurchaseRequest) error {
	return waylogv2.StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		resp, err := h.callPayment(ctx, reqBody)
		if err != nil {
			return err
		}
		if resp >= http.StatusInternalServerError {
			werr := waylogv2.NewError("PMT_502", waylogv2.WithReason("upstream gateway 5xx"))
			waylogv2.From(ctx).Warn("retrying payment", waylogv2.F{"attempt": 1, "max_attempts": 2})
			waylogv2.From(ctx).Error("upstream gateway 5xx", werr, waylogv2.F{"status": resp})
			return werr
		}
		return nil
	})
}

func (h *CheckoutHandler) callPayment(ctx context.Context, reqBody PurchaseRequest) (int, error) {
	raw, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.paymentURL+"/pay", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.paymentClient.Do(req)
	if err != nil {
		return 0, waylogv2.NewError("PMT_502", waylogv2.WithReason("payment service unavailable"))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
