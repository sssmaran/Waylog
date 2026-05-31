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
			Transport: wayloghttp.NewTransport(demoHTTPTransport(), "payment"),
		},
		dbClient: &http.Client{
			Transport: wayloghttp.NewTransport(demoHTTPTransport(), "db"),
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

	if reqBody.Scenario == ScenarioCheckoutPanic {
		// Real recoverable panic inside the instrumented request. The Waylog
		// HTTP middleware recovers it -> emits a failed checkout WideEvent and,
		// with runtime hooks on, posts a go-sdk "runtime" panic signal.
		panic("checkout: simulated panic charging payment (demo)")
	}

	if reqBody.Scenario == ScenarioSuppressedPayment502 {
		h.serveSuppressedPayment(w, reqBody, ctx)
		return
	}

	if err := h.validateCart(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "checkout validation failed"))
		return
	}
	if err := h.loadCart(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, err.Error()))
		return
	}
	if err := h.reserveInventory(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, err.Error()))
		return
	}
	if err := h.chargePayment(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "payment gateway failure"))
		return
	}
	if err := h.commitOrder(ctx, reqBody); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, err.Error()))
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

func (h *CheckoutHandler) validateCart(ctx context.Context, reqBody PurchaseRequest) error {
	return waylogv2.StepVoid(ctx, "cart.validate", func(ctx context.Context) error {
		if reqBody.Scenario == ScenarioCheckoutError {
			return waylogv2.NewError("CHK_500", waylogv2.WithReason("invalid cart state: missing tenant binding"))
		}
		waylogv2.From(ctx).Info("cart validated", waylogv2.F{"sku": reqBody.SKU})
		return nil
	})
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
		if resp.StatusCode == http.StatusNotFound {
			return waylogv2.NewError("CART_NOT_FOUND", waylogv2.WithReason("cart record not found for sku"))
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			return waylogv2.NewError("DB_503", waylogv2.WithReason("database unavailable"))
		}
		waylogv2.From(ctx).Info("cart loaded", waylogv2.F{
			"sku":              reqBody.SKU,
			"items_n":          3,
			"cart_value_cents": 4299,
		})
		return nil
	})
}

func (h *CheckoutHandler) reserveInventory(ctx context.Context, reqBody PurchaseRequest) error {
	return waylogv2.StepVoid(ctx, "inventory.reserve", func(ctx context.Context) error {
		if reqBody.Scenario == ScenarioInventory503 {
			return waylogv2.NewError("INV_503", waylogv2.WithReason("inventory service unavailable"))
		}
		waylogv2.From(ctx).Info("inventory reserved", waylogv2.F{
			"sku":            reqBody.SKU,
			"reservation_id": "res-" + reqBody.SKU,
		})
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

func (h *CheckoutHandler) commitOrder(ctx context.Context, reqBody PurchaseRequest) error {
	return waylogv2.StepVoid(ctx, "order.commit", func(ctx context.Context) error {
		waylogv2.From(ctx).Info("order committed", waylogv2.F{
			"sku":      reqBody.SKU,
			"order_id": "ord-" + reqBody.SKU,
		})
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
