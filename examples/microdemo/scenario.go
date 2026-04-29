package microdemo

import (
	"context"
	"errors"

	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

var errUnknownScenario = errors.New("unknown demo scenario")

func normalizeScenario(s string) string {
	switch s {
	case "", ScenarioHappy:
		return ScenarioHappy
	case ScenarioPayment502:
		return ScenarioPayment502
	case ScenarioSuppressedPayment502:
		return ScenarioSuppressedPayment502
	default:
		return ""
	}
}

func legacyForceScenario(force string) string {
	switch force {
	case "", "success":
		return ScenarioHappy
	case "payment_fail", "payment_retry":
		return ScenarioPayment502
	default:
		return ""
	}
}

func setDemoFields(ctx context.Context, service string, req PurchaseRequest) {
	waylogv2.SetField(ctx, "user", map[string]any{
		"id":     demoUserID,
		"tier":   "standard",
		"region": "us-east-1",
	})
	waylogv2.SetField(ctx, "demo", map[string]any{
		"scenario": req.Scenario,
		"sku":      req.SKU,
		"service":  service,
	})
}

func response(ctx context.Context, success bool, req PurchaseRequest, errMsg string) map[string]any {
	out := map[string]any{
		"success":  success,
		"trace_id": waylogv2.TraceID(ctx),
		"scenario": req.Scenario,
		"sku":      req.SKU,
	}
	if errMsg != "" {
		out["error"] = errMsg
	}
	return out
}
