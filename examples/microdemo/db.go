package microdemo

import (
	"context"
	"encoding/json"
	"net/http"

	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

type DBHandler struct{}

func NewDBHandler() *DBHandler {
	return &DBHandler{}
}

func (h *DBHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqBody, err := parsePurchaseRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	setDemoFields(ctx, "db", reqBody)

	if reqBody.Scenario == ScenarioSuppressedPayment502 {
		w.WriteHeader(http.StatusOK)
		waylogv2.Suppress(ctx)
		_ = json.NewEncoder(w).Encode(response(ctx, true, reqBody, ""))
		return
	}

	if reqBody.Scenario == ScenarioDBMiss {
		_ = waylogv2.StepVoid(ctx, "cart.lookup", func(ctx context.Context) error {
			return waylogv2.NewError("CART_NOT_FOUND", waylogv2.WithReason("cart record not found for sku"))
		})
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "cart record not found"))
		return
	}

	if err := loadCart(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "database unavailable"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response(ctx, true, reqBody, ""))
}

func loadCart(ctx context.Context) error {
	return waylogv2.StepVoid(ctx, "cart.lookup", func(ctx context.Context) error {
		return nil
	})
}
