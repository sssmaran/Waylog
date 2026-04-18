package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// validEvent returns a minimal valid WideEvent for testing.
func validEvent() WideEvent {
	return WideEvent{
		SchemaVersion: "1.0",
		EventName:     "test.request",
		Timestamp:     time.Now(),
		User:          UserContext{ID: "u1"},
		Request:       RequestContext{TraceID: "abc123"},
		System:        SystemContext{Service: "svc", Env: "dev"},
		Outcome:       OutcomeContext{Success: true, StatusCode: 200},
	}
}

func TestValidate_SchemaVersions(t *testing.T) {
	tests := []struct {
		version string
		wantErr string // substring, empty = no error
	}{
		{"1.0", ""},
		{"1.1", ""},
		{"1.99", ""},
		{"2.0", "unsupported schema_version: 2.0"},
		{"0.9", "unsupported schema_version: 0.9"},
		{"abc", "invalid schema_version format"},
		{"1", "invalid schema_version format"},
		{"1.0.1", "invalid schema_version format"},
		{"1.x", "invalid schema_version minor"},
		{"1.", "invalid schema_version minor"},
		{"", "schema_version is required"},
	}
	for _, tt := range tests {
		ev := validEvent()
		ev.SchemaVersion = tt.version
		err := ev.Validate()
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("version %q: unexpected error: %v", tt.version, err)
			}
		} else {
			if err == nil {
				t.Errorf("version %q: expected error containing %q, got nil", tt.version, tt.wantErr)
			} else if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("version %q: error %q does not contain %q", tt.version, err, tt.wantErr)
			}
		}
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	// Missing event_name
	ev := validEvent()
	ev.EventName = ""
	if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), "event_name") {
		t.Errorf("expected event_name error, got %v", err)
	}

	// Missing trace_id
	ev = validEvent()
	ev.Request.TraceID = ""
	if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), "trace_id") {
		t.Errorf("expected trace_id error, got %v", err)
	}

	// ParentSpanID set without SpanID
	ev = validEvent()
	ev.Request.ParentSpanID = "parent1"
	ev.Request.SpanID = ""
	if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), "span_id") {
		t.Errorf("expected span_id error, got %v", err)
	}

	// Failure without error context
	ev = validEvent()
	ev.Outcome.Success = false
	ev.Error = nil
	if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), "error must be set") {
		t.Errorf("expected error context error, got %v", err)
	}

	// Failure with empty error code
	ev = validEvent()
	ev.Outcome.Success = false
	ev.Error = &ErrorContext{Message: "fail"}
	if err := ev.Validate(); err == nil || !strings.Contains(err.Error(), "error.code") {
		t.Errorf("expected error.code error, got %v", err)
	}
}

func TestIsError(t *testing.T) {
	ev := validEvent()
	if ev.IsError() {
		t.Error("success event should not be error")
	}

	ev.Outcome.Success = false
	if !ev.IsError() {
		t.Error("failed event should be error")
	}

	ev = validEvent()
	ev.Outcome.StatusCode = 500
	if !ev.IsError() {
		t.Error("500 status should be error")
	}

	ev = validEvent()
	ev.Error = &ErrorContext{Code: "E1"}
	if !ev.IsError() {
		t.Error("event with error context should be error")
	}
}

func TestValidate_ReturnsValidationErrors(t *testing.T) {
	ev := WideEvent{} // completely empty
	err := ev.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
}

func TestValidationErrors_HasOnly(t *testing.T) {
	ev := WideEvent{
		SchemaVersion: "1.0",
		EventName:     "svc.request",
		Timestamp:     time.Now(),
		// User.ID deliberately empty
		Request: RequestContext{TraceID: "aaaabbbbccccddddeeeeffffaaaabbbb", SpanID: "aaaabbbbccccdddd"},
		System:  SystemContext{Service: "svc", Env: "prod"},
		Outcome: OutcomeContext{Success: true, StatusCode: 200},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatal("expected error for missing user.id")
	}
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if !ve.HasOnly("user.id") {
		t.Errorf("expected HasOnly(user.id)=true, got false; errors: %v", ve)
	}
	if ve.HasOnly("system.service") {
		t.Error("expected HasOnly(system.service)=false")
	}
}

func TestValidationErrors_HasOnly_MultipleErrors(t *testing.T) {
	ev := WideEvent{
		SchemaVersion: "1.0",
		EventName:     "svc.request",
		Timestamp:     time.Now(),
		// User.ID empty AND system.service empty
		Request: RequestContext{TraceID: "aaaabbbbccccddddeeeeffffaaaabbbb"},
		System:  SystemContext{Env: "prod"},
		Outcome: OutcomeContext{Success: true, StatusCode: 200},
	}
	err := ev.Validate()
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if ve.HasOnly("user.id") {
		t.Error("expected HasOnly(user.id)=false when multiple errors present")
	}
}

// --- Schema 1.1 (Mark II) ---

func TestSchemaVersion_BumpedTo11(t *testing.T) {
	if SchemaVersion != "1.1" {
		t.Errorf("expected SchemaVersion=1.1 (Mark II), got %s", SchemaVersion)
	}
}

func TestValidate_BackCompat10Events(t *testing.T) {
	// Existing 1.0 events must keep validating after the const bump.
	ev := validEvent()
	ev.SchemaVersion = "1.0"
	if err := ev.Validate(); err != nil {
		t.Errorf("1.0 events must still validate after const bump: %v", err)
	}
}

func TestErrorContext_StructuredFields(t *testing.T) {
	ev := validEvent()
	ev.Outcome.Success = false
	ev.Outcome.StatusCode = 500
	ev.Error = &ErrorContext{
		Code:    "PMT_502",
		Path:    "https://runbooks.internal/pmt-502",
		Message: "Payment gateway timeout",
		Reason:  "Upstream gateway did not respond within 5s",
	}
	if err := ev.Validate(); err != nil {
		t.Errorf("structured error must validate: %v", err)
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WideEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("error context lost in round-trip")
	}
	if got.Error.Reason != ev.Error.Reason || got.Error.Path != ev.Error.Path {
		t.Errorf("structured fields not round-tripped: got %+v", got.Error)
	}
}

func TestErrorContext_StructuredFieldsOmitemptyWhenAbsent(t *testing.T) {
	ev := validEvent()
	ev.Outcome.Success = false
	ev.Outcome.StatusCode = 500
	ev.Error = &ErrorContext{Code: "X", Message: "y"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, field := range []string{`"reason"`, `"path"`} {
		if strings.Contains(s, field) {
			t.Errorf("expected %s omitted when empty, found in: %s", field, s)
		}
	}
}

func TestWideEvent_ParentRequestID(t *testing.T) {
	ev := validEvent()
	ev.ParentRequestID = "req_abc123"
	if err := ev.Validate(); err != nil {
		t.Errorf("parent_request_id should validate: %v", err)
	}

	b, _ := json.Marshal(ev)
	var got WideEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ParentRequestID != "req_abc123" {
		t.Errorf("parent_request_id not round-tripped: got %q", got.ParentRequestID)
	}
}

func TestWideEvent_Metadata(t *testing.T) {
	ev := validEvent()
	ev.Metadata = map[string]any{"cart_total": 99.50, "items": 3, "promo": "SAVE10"}
	if err := ev.Validate(); err != nil {
		t.Errorf("metadata should validate: %v", err)
	}

	b, _ := json.Marshal(ev)
	var got WideEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Metadata["promo"] != "SAVE10" {
		t.Errorf("metadata not round-tripped: got %v", got.Metadata)
	}
}

func TestWideEvent_RetryContext(t *testing.T) {
	ev := validEvent()
	ev.Request.Attempt = 2
	ev.Retry = &RetryContext{
		Of:                3,
		PreviousAttemptID: "req_prev_attempt",
	}
	if err := ev.Validate(); err != nil {
		t.Errorf("retry should validate: %v", err)
	}

	b, _ := json.Marshal(ev)
	var got WideEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Retry == nil {
		t.Fatal("retry not round-tripped")
	}
	if got.Retry.Of != 3 || got.Retry.PreviousAttemptID != "req_prev_attempt" {
		t.Errorf("retry fields wrong: got %+v", got.Retry)
	}
}

func TestValidate_RetryOfMustBeZeroOrGteAttempt(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		retry   *RetryContext
		wantErr bool
	}{
		{"no retry context", 0, nil, false},
		{"no retry context with attempt", 1, nil, false},
		{"retry of=0 means unknown total", 1, &RetryContext{Of: 0}, false},
		{"attempt 2 of 3 ok", 2, &RetryContext{Of: 3}, false},
		{"attempt 3 of 3 ok", 3, &RetryContext{Of: 3}, false},
		{"of less than attempt invalid", 3, &RetryContext{Of: 2}, true},
		{"of=1 with attempt=2 invalid", 2, &RetryContext{Of: 1}, true},
		{"previous_attempt_id only is fine", 2, &RetryContext{PreviousAttemptID: "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := validEvent()
			ev.Request.Attempt = tt.attempt
			ev.Retry = tt.retry
			err := ev.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidate_OTLPCompat_NewFieldsAllOptional(t *testing.T) {
	// OTLP-style event: omits user.id (handled by HasOnly suppression upstream)
	// and never sets the new 1.1 fields. Validate() should not fault on missing
	// 1.1 fields — only on the user.id we already know about.
	ev := WideEvent{
		SchemaVersion: SchemaVersion,
		EventName:     "checkout.request",
		Timestamp:     time.Now(),
		Request:       RequestContext{TraceID: "aaaabbbbccccddddeeeeffffaaaabbbb", SpanID: "aaaabbbbccccdddd"},
		System:        SystemContext{Service: "checkout", Env: "prod"},
		Outcome:       OutcomeContext{Success: true, StatusCode: 200},
	}
	err := ev.Validate()
	if err == nil {
		t.Fatal("expected user.id validation error from OTLP-style event")
	}
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if !ve.HasOnly("user.id") {
		t.Errorf("OTLP event should fail only on user.id, got: %v", ve)
	}
}

func TestValidate_HTTPMethodAndRouteTemplate(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		route   string
		wantErr string
	}{
		{"valid GET", "GET", "/users/{id}", ""},
		{"valid POST with route", "POST", "/users", ""},
		{"both empty", "", "", ""},
		{"method only", "GET", "", ""},
		{"route only", "", "/health", ""},
		{"invalid method", "INVALID", "", "not a valid HTTP method"},
		{"lowercase method", "get", "", "not a valid HTTP method"},
		{"missing leading slash", "GET", "users", "must start with /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := validEvent()
			ev.Request.HTTPMethod = tt.method
			ev.Request.RouteTemplate = tt.route
			err := ev.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err, tt.wantErr)
				}
			}
		})
	}
}
