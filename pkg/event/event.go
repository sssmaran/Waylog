package event

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = "1.0"

type WideEvent struct {
	SchemaVersion string    `json:"schema_version"`
	EventName     string    `json:"event_name"`
	Timestamp     time.Time `json:"timestamp"`

	User    UserContext    `json:"user"`
	Request RequestContext `json:"request"`
	System  SystemContext  `json:"system"`

	Outcome OutcomeContext `json:"outcome"`
	Error   *ErrorContext  `json:"error,omitempty"`
	Metrics MetricsContext `json:"metrics"`
}

type UserContext struct {
	ID     string `json:"id"`
	Tier   string `json:"tier"`   // e.g., "free", "premium"
	Region string `json:"region"` // optional but useful at scale
	VIP    bool   `json:"vip"`
}

type RequestContext struct {
	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`

	HTTPMethod    string `json:"http_method,omitempty"`
	RouteTemplate string `json:"route_template,omitempty"`

	Flow         string   `json:"flow"`
	FeatureFlags []string `json:"feature_flags"`

	CorrelationID string `json:"correlation_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	TransportKind string `json:"transport_kind,omitempty"`
}

type SystemContext struct {
	Service           string `json:"service"`       // e.g., "payment-service"
	Version           string `json:"version"`       // e.g., "1.9.2"
	DeploymentID      string `json:"deployment_id"` // e.g., "deploy_2026_01_05"
	Env               string `json:"env"`           // "dev", "staging", "prod"
	DownstreamService string `json:"downstream_service,omitempty"`
	CallerService     string `json:"caller_service,omitempty"`
}

type OutcomeContext struct {
	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code"`
	Kind       string `json:"kind"` // "http", "job", "rpc"
}

type ErrorContext struct {
	Code    string `json:"code"`    // stable error code (PMT_402)
	Message string `json:"message"` // short, not a stack dump
}

type MetricsContext struct {
	LatencyMs int64 `json:"latency_ms"`
}

func (e WideEvent) Validate() error {
	var errs ValidationErrors

	if e.SchemaVersion == "" {
		errs = append(errs, ValidationError{Field: "schema_version", Message: "schema_version is required"})
	} else if err := supportedSchema(e.SchemaVersion); err != nil {
		errs = append(errs, ValidationError{Field: "schema_version", Message: err.Error()})
	}
	if e.EventName == "" {
		errs = append(errs, ValidationError{Field: "event_name", Message: "event_name is required"})
	}
	if e.Timestamp.IsZero() {
		errs = append(errs, ValidationError{Field: "timestamp", Message: "timestamp is required"})
	}
	if e.User.ID == "" {
		errs = append(errs, ValidationError{Field: "user.id", Message: "user.id is required"})
	}
	if e.Request.TraceID == "" {
		errs = append(errs, ValidationError{Field: "request.trace_id", Message: "request.trace_id is required"})
	}
	if e.Request.ParentSpanID != "" && e.Request.SpanID == "" {
		errs = append(errs, ValidationError{Field: "request.span_id", Message: "request.span_id is required when request.parent_span_id is set"})
	}
	if e.Request.HTTPMethod != "" && !validHTTPMethod(e.Request.HTTPMethod) {
		errs = append(errs, ValidationError{Field: "request.http_method", Message: fmt.Sprintf("request.http_method %q is not a valid HTTP method", e.Request.HTTPMethod)})
	}
	if e.Request.RouteTemplate != "" && e.Request.RouteTemplate[0] != '/' {
		errs = append(errs, ValidationError{Field: "request.route_template", Message: "request.route_template must start with /"})
	}
	if e.System.Service == "" {
		errs = append(errs, ValidationError{Field: "system.service", Message: "system.service is required"})
	}
	if e.System.Env == "" {
		errs = append(errs, ValidationError{Field: "system.env", Message: "system.env is required"})
	}
	if e.Outcome.StatusCode == 0 {
		errs = append(errs, ValidationError{Field: "outcome.status_code", Message: "outcome.status_code is required"})
	}
	if !e.Outcome.Success {
		if e.Error == nil {
			errs = append(errs, ValidationError{Field: "error", Message: "error must be set when outcome.success=false"})
		} else if e.Error.Code == "" {
			errs = append(errs, ValidationError{Field: "error.code", Message: "error.code is required for failures"})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func (e WideEvent) IsError() bool {
	return !e.Outcome.Success || e.Outcome.StatusCode >= 500 || e.Error != nil
}

func validHTTPMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return true
	}
	return false
}

func supportedSchema(v string) error {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid schema_version format: %s (expected major.minor)", v)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("invalid schema_version major: %s", v)
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return fmt.Errorf("invalid schema_version minor: %s", v)
	}
	if major != 1 {
		return fmt.Errorf("unsupported schema_version: %s (supported: 1.x)", v)
	}
	return nil
}
