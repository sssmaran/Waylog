package eventv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// TestV11BridgeSchemaAcceptsCurrentV1Event proves the bridge schema at
// docs/schema/v1.1.json is in lockstep with the actual v1 event.WideEvent
// type. If the v1.1 type changes during the bridge window, this test will
// fail until the schema is updated to match.
func TestV11BridgeSchemaAcceptsCurrentV1Event(t *testing.T) {
	e := event.WideEvent{
		SchemaVersion: "1.1",
		EventName:     "checkout.request",
		Timestamp:     time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC),
		User: event.UserContext{
			ID:   "u_123",
			Tier: "standard",
		},
		Request: event.RequestContext{
			TraceID:       "11111111111111111111111111111111",
			SpanID:        "1111111111111111",
			HTTPMethod:    "POST",
			RouteTemplate: "/checkout",
			Flow:          "purchase",
			FeatureFlags:  []string{},
		},
		System: event.SystemContext{
			Service: "checkout",
			Env:     "test",
			Version: "1.0.0",
		},
		Outcome: event.OutcomeContext{
			Success:    true,
			StatusCode: 200,
			Kind:       "http",
		},
	}

	raw, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("marshal v1 event: %v", err)
	}

	schemaPath, err := filepath.Abs("../../../docs/schema/v1.1.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(schemaPath); err != nil {
		t.Fatalf("v1.1 schema missing at %s: %v", schemaPath, err)
	}

	sch, err := CompileSchema(schemaPath)
	if err != nil {
		t.Fatalf("compile v1.1 schema: %v", err)
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if err := sch.Validate(v); err != nil {
		t.Fatalf("v1.1 bridge schema must accept current v1 WideEvent: %v\npayload: %s", err, raw)
	}
}

// TestV11BridgeSchemaRejectsMissingTraceID is a sanity check that the bridge
// schema is not vacuous — it should still reject a v1 event that lacks the
// one field every consumer needs (request.trace_id).
func TestV11BridgeSchemaRejectsMissingTraceID(t *testing.T) {
	bad := map[string]any{
		"schema_version": "1.1",
		"event_name":     "checkout.request",
		"timestamp":      "2026-04-25T14:00:00Z",
		"request":        map[string]any{}, // missing trace_id
		"system":         map[string]any{"service": "checkout", "env": "test"},
		"outcome":        map[string]any{"success": true, "status_code": 200, "kind": "http"},
	}

	schemaPath, _ := filepath.Abs("../../../docs/schema/v1.1.json")
	sch, err := CompileSchema(schemaPath)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := sch.Validate(bad); err == nil {
		t.Fatal("v1.1 schema must reject events with empty request (no trace_id)")
	}
}

// TestV20SchemaRequiresStatus locks in that the v2.0 schema treats status as a
// required top-level field, matching the rest of the implementation and the
// product spec's triage model.
func TestV20SchemaRequiresStatus(t *testing.T) {
	schemaPath, err := filepath.Abs("../../../docs/schema/v2.0.json")
	if err != nil {
		t.Fatal(err)
	}
	sch, err := CompileSchema(schemaPath)
	if err != nil {
		t.Fatalf("compile v2.0 schema: %v", err)
	}

	// All currently-required fields except status. Should be rejected.
	bad := map[string]any{
		"schema_version": "2.0",
		"event_id":       "00000000-0000-4000-8000-000000000099",
		"ts_start":       "2026-04-25T14:00:00.000Z",
		"ts_end":         "2026-04-25T14:00:00.010Z",
		"kind":           "http",
		"service":        "checkout",
		"env":            "test",
		"trace_id":       "99999999999999999999999999999999",
	}
	if err := sch.Validate(bad); err == nil {
		t.Fatal("v2.0 schema must reject events missing status")
	}
}
