package microdemo

import (
	"context"
	"encoding/json"
	"net/http"

	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

type PaymentHandler struct{}

func NewPaymentHandler() *PaymentHandler {
	return &PaymentHandler{}
}

func (h *PaymentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqBody, err := parsePurchaseRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	setDemoFields(ctx, "payment", reqBody)

	switch reqBody.Scenario {
	case ScenarioHappy:
		chargeAcquirer(ctx, false)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response(ctx, true, reqBody, ""))
	case ScenarioPayment502:
		chargeAcquirer(ctx, true)
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "upstream gateway 5xx"))
	case ScenarioSuppressedPayment502:
		w.WriteHeader(http.StatusBadGateway)
		waylogv2.Suppress(ctx)
		_ = json.NewEncoder(w).Encode(response(ctx, false, reqBody, "known payment gateway issue suppressed"))
	default:
		http.Error(w, errUnknownScenario.Error(), http.StatusBadRequest)
	}
}

func chargeAcquirer(ctx context.Context, gatewayFailure bool) {
	_ = waylogv2.StepVoid(ctx, "acquirer.charge", func(ctx context.Context) error {
		if gatewayFailure {
			waylogv2.From(ctx).Warn("acquirer returned gateway 5xx", waylogv2.F{"code": "PMT_502"})
		}
		return nil
	})
}
