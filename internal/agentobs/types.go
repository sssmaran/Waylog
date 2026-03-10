package agentobs

import (
	"fmt"
	"time"
)

const (
	EventRunStart         = "run.start"
	EventRunEnd           = "run.end"
	EventSessionStart     = "session.start"
	EventSessionEnd       = "session.end"
	EventSessionHeartbeat = "session.heartbeat"
	EventStepStart        = "step.start"
	EventStepEnd          = "step.end"

	StateActive    = "active"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateAbandoned = "abandoned"
)

type AgentEvent struct {
	EventID         string    `json:"event_id"`
	RunID           string    `json:"run_id"`
	SessionID       string    `json:"session_id,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	TriggerStepID   string    `json:"trigger_step_id,omitempty"`
	StepID          string    `json:"step_id,omitempty"`
	StepName        string    `json:"step_name,omitempty"`
	StepIndex       int       `json:"step_index,omitempty"`
	EventType       string    `json:"event_type"`
	Timestamp       time.Time `json:"timestamp"`
	SchemaVersion   string    `json:"schema_version"`
	AgentName       string    `json:"agent_name,omitempty"`
	AgentVersion    string    `json:"agent_version,omitempty"`
	Prompt          string    `json:"prompt,omitempty"`
	Model           string    `json:"model,omitempty"`
	TokensIn        int       `json:"tokens_in,omitempty"`
	TokensOut       int       `json:"tokens_out,omitempty"`
	ToolName        string    `json:"tool_name,omitempty"`
	ToolInput       string    `json:"tool_input,omitempty"`
	ToolOutput      string    `json:"tool_output,omitempty"`
	ToolError       string    `json:"tool_error,omitempty"`
	LatencyMs       int64     `json:"latency_ms,omitempty"`
	TotalSteps      int       `json:"total_steps,omitempty"`
	TotalTokens     int       `json:"total_tokens,omitempty"`
	Success         bool      `json:"success,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
}

// Validate checks required fields and per-type constraints.
func (e *AgentEvent) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("event_id required")
	}
	if e.RunID == "" {
		return fmt.Errorf("run_id required")
	}
	if e.SchemaVersion != "1.0" {
		return fmt.Errorf("schema_version must be \"1.0\", got %q", e.SchemaVersion)
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("timestamp required")
	}

	switch e.EventType {
	case EventRunStart, EventRunEnd:
		// base fields only
	case EventSessionStart:
		if e.SessionID == "" {
			return fmt.Errorf("session_id required for %s", e.EventType)
		}
		if e.AgentName == "" {
			return fmt.Errorf("agent_name required for %s", e.EventType)
		}
	case EventSessionEnd, EventSessionHeartbeat:
		if e.SessionID == "" {
			return fmt.Errorf("session_id required for %s", e.EventType)
		}
	case EventStepStart:
		if e.SessionID == "" {
			return fmt.Errorf("session_id required for %s", e.EventType)
		}
		if e.StepID == "" {
			return fmt.Errorf("step_id required for %s", e.EventType)
		}
		if e.StepIndex < 0 {
			return fmt.Errorf("step_index must be >= 0 for %s", e.EventType)
		}
	case EventStepEnd:
		if e.SessionID == "" {
			return fmt.Errorf("session_id required for %s", e.EventType)
		}
		if e.StepID == "" {
			return fmt.Errorf("step_id required for %s", e.EventType)
		}
		if e.StepIndex < 0 {
			return fmt.Errorf("step_index must be >= 0 for %s", e.EventType)
		}
		if e.LatencyMs <= 0 {
			return fmt.Errorf("latency_ms required for %s", e.EventType)
		}
	default:
		return fmt.Errorf("unknown event_type %q", e.EventType)
	}
	return nil
}

// IsTerminalState returns true for completed or failed states.
func IsTerminalState(state string) bool {
	return state == StateCompleted || state == StateFailed
}
