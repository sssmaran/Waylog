package checkout

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/event"
	"github.com/sssmaran/WaylogCLI/internal/trace"
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
	ctx := r.Context()
	user := randomUser()

	tc, ok := trace.FromContext(ctx)
	if !ok {
		tc = trace.NewRoot()
	}
	traceID := tc.TraceID
	rootSpanID := tc.SpanID
	parentSpanID := tc.ParentSpanID

	req := CheckoutRequest{User: user}
	result := h.svc.Process(req)

	// Root span: checkout-service
	h.emitSpan(
		ctx,
		traceID,
		rootSpanID,
		parentSpanID,
		"checkout-service",
		result,
		user,
	)

	// Simulated downstream: payment-service
	paymentSpan := trace.NewChild(traceID, rootSpanID)
	paymentResult := result // reuse result for now (can diverge later)

	h.emitSpan(
		ctx,
		traceID,
		paymentSpan.SpanID,
		paymentSpan.ParentSpanID,
		"payment-service",
		paymentResult,
		user,
	)

	// Simulated downstream: inventory-service
	inventorySpan := trace.NewChild(traceID, rootSpanID)
	inventoryResult := result

	h.emitSpan(
		ctx,
		traceID,
		inventorySpan.SpanID,
		inventorySpan.ParentSpanID,
		"inventory-service",
		inventoryResult,
		user,
	)

	w.WriteHeader(result.StatusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"success": result.Success,
		"trace_id": traceID,
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

func (h *Handler) emitSpan(
	ctx context.Context,
	traceID string,
	spanID string,
	parentSpanID string,
	service string,
	result CheckoutResult,
	user User,
) {
	ev := event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     "checkout_span",
		Timestamp:     time.Now().UTC(),

		User: event.UserContext{
			ID:     user.ID,
			Tier:   user.Tier,
			Region: user.Region,
			VIP:    user.VIP,
		},

		Request: event.RequestContext{
			TraceID:      traceID,
			SpanID:       spanID,
			ParentSpanID: parentSpanID,
			Flow:         result.Flow,
			FeatureFlags: result.Flags,
		},

		System: event.SystemContext{
			Service: service,
			Version: "0.1.0",
			Env:     "dev",
		},

		Outcome: event.OutcomeContext{
			Success:    result.Success,
			StatusCode: result.StatusCode,
			Kind:       "internal",
		},

		Error: buildError(result),

		Metrics: event.MetricsContext{
			LatencyMs: result.LatencyMs,
		},
	}

	_ = h.events.Emit(ctx, ev)
}
