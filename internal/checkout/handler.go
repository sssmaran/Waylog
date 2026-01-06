package checkout

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/event"
	"github.com/sssmaran/WaylogCLI/pkg/sdk"
)

type Handler struct {
	svc    *Service
	events *sdk.Client
}

func NewHandler(svc *Service, events *sdk.Client) *Handler {
	return &Handler{svc: svc, events: events}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user := randomUser()

	req := CheckoutRequest{User: user}
	result := h.svc.Process(req)

	ev := event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     "checkout_request",
		Timestamp:     time.Now().UTC(),

		User: event.UserContext{
			ID:     user.ID,
			Tier:   user.Tier,
			Region: user.Region,
			VIP:    user.VIP,
		},
		Request: event.RequestContext{
			TraceID:      randID("trace"),
			Flow:         result.Flow,
			FeatureFlags: result.Flags,
		},
		System: event.SystemContext{
			Service:      "checkout-service",
			Version:      "0.1.0",
			DeploymentID: "local-dev",
			Env:          "dev",
		},
		Outcome: event.OutcomeContext{
			Success:    result.Success,
			StatusCode: result.StatusCode,
			Kind:       "http",
		},
		Error: buildError(result),
		Metrics: event.MetricsContext{
			LatencyMs: result.LatencyMs,
		},
	}

	_ = h.events.Emit(context.Background(), ev)

	w.WriteHeader(result.StatusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"success": result.Success,
	})
}

func buildError(r CheckoutResult) *event.ErrorContext {
	if r.Success {
		return nil
	}
	return &event.ErrorContext{
		Code:    r.ErrorCode,
		Message: r.ErrorMsg,
	}
}
