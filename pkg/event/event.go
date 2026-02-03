package event

import (
	"errors"
	"fmt"
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

	Flow         string   `json:"flow"`
	FeatureFlags []string `json:"feature_flags"`
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
	if e.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version: %s", e.SchemaVersion)
	}
	if e.EventName == "" {
		return errors.New("event_name is required")
	}
	if e.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}

	if e.User.ID == "" {
		return errors.New("user.id is required")
	}
	if e.Request.TraceID == "" {
		return errors.New("request.trace_id is required")
	}
	// If ParentSpanID is set, SpanID must be set
	if e.Request.ParentSpanID != "" && e.Request.SpanID == "" {
		return errors.New("request.span_id is required when request.parent_span_id is set")
	}

	if e.System.Service == "" {
		return errors.New("system.service is required")
	}
	if e.System.Env == "" {
		return errors.New("system.env is required")
	}
	if e.Outcome.StatusCode == 0 {
		return errors.New("outcome.status_code is required")
	}

	// If success=false, error must exist and be coded.
	if !e.Outcome.Success {
		if e.Error == nil {
			return errors.New("error must be set when outcome.success=false")
		}
		if e.Error.Code == "" {
			return errors.New("error.code is required for failures")
		}
	}

	return nil
}

func (e WideEvent) IsError() bool {
	return !e.Outcome.Success || e.Outcome.StatusCode >= 500 || e.Error != nil
}
