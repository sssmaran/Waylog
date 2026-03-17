package event

import (
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
