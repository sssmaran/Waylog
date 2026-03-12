package agentobs

import (
	"testing"
	"time"
)

func validBase() AgentEvent {
	return AgentEvent{
		EventID:       "evt-001",
		RunID:         "run-001",
		SchemaVersion: "1.0",
		Timestamp:     time.Now(),
	}
}

func TestAgentEvent_Validate_SessionStart(t *testing.T) {
	e := validBase()
	e.EventType = EventSessionStart
	e.SessionID = "sess-001"
	e.AgentName = "test-agent"
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestAgentEvent_Validate_SessionStart_MissingAgentName(t *testing.T) {
	e := validBase()
	e.EventType = EventSessionStart
	e.SessionID = "sess-001"
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for missing agent_name")
	}
}

func TestAgentEvent_Validate_StepEnd_MissingStepID(t *testing.T) {
	e := validBase()
	e.EventType = EventStepEnd
	e.SessionID = "sess-001"
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for missing step_id")
	}
}

func TestAgentEvent_Validate_RunStart(t *testing.T) {
	e := validBase()
	e.EventType = EventRunStart
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestAgentEvent_Validate_BadSchemaVersion(t *testing.T) {
	e := validBase()
	e.SchemaVersion = "2.0"
	e.EventType = EventRunStart
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for schema version 2.0")
	}
}

func TestAgentEvent_Validate_EmptySchemaVersion(t *testing.T) {
	e := validBase()
	e.SchemaVersion = ""
	e.EventType = EventRunStart
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for empty schema version")
	}
}

func TestAgentEvent_Validate_UnknownType(t *testing.T) {
	e := validBase()
	e.EventType = "unknown.type"
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for unknown event type")
	}
}

func TestAgentEvent_Validate_StepStart_NegativeStepIndex(t *testing.T) {
	e := validBase()
	e.EventType = EventStepStart
	e.SessionID = "sess-001"
	e.StepID = "step-001"
	e.StepIndex = -1
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for negative step_index")
	}
}

func TestAgentEvent_Validate_StepEnd_NegativeLatencyMs(t *testing.T) {
	e := validBase()
	e.EventType = EventStepEnd
	e.SessionID = "sess-001"
	e.StepID = "step-001"
	e.StepIndex = 0
	e.LatencyMs = -1
	if err := e.Validate(); err == nil {
		t.Fatal("expected error for negative latency_ms on step.end")
	}
}

func TestAgentEvent_Validate_StepEnd_ZeroLatencyMs(t *testing.T) {
	e := validBase()
	e.EventType = EventStepEnd
	e.SessionID = "sess-001"
	e.StepID = "step-001"
	e.StepIndex = 0
	e.LatencyMs = 0
	if err := e.Validate(); err != nil {
		t.Fatalf("zero latency_ms should be valid, got: %v", err)
	}
}

func TestAgentEvent_Validate_StepEnd_Valid(t *testing.T) {
	e := validBase()
	e.EventType = EventStepEnd
	e.SessionID = "sess-001"
	e.StepID = "step-001"
	e.StepIndex = 0
	e.LatencyMs = 100
	if err := e.Validate(); err != nil {
		t.Fatalf("expected valid step.end, got %v", err)
	}
}
