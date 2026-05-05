package signals

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Type string

const (
	TypeDeploy      Type = "deploy"
	TypeRuntime     Type = "runtime"
	TypeHealthcheck Type = "healthcheck"
	TypeDependency  Type = "dependency"
	TypeConfig      Type = "config"
	TypeAlert       Type = "alert"
)

func (t Type) Valid() bool {
	switch t {
	case TypeDeploy, TypeRuntime, TypeHealthcheck, TypeDependency, TypeConfig, TypeAlert:
		return true
	default:
		return false
	}
}

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	default:
		return false
	}
}

type Signal struct {
	SignalID   string         `json:"signal_id"`
	Type       Type           `json:"type"`
	Source     string         `json:"source"`
	Service    string         `json:"service"`
	Env        string         `json:"env"`
	Severity   Severity       `json:"severity"`
	Reason     string         `json:"reason"`
	Message    string         `json:"message,omitempty"`
	Resource   map[string]any `json:"resource,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	ReceivedAt time.Time      `json:"received_at"`
	Extra      map[string]any `json:"-"`
}

func NewSignalID() string {
	return "sig_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (s *Signal) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = Signal{}
	extra := map[string]any{}
	for key, value := range raw {
		switch key {
		case "signal_id":
			if err := json.Unmarshal(value, &s.SignalID); err != nil {
				return fmt.Errorf("signal_id: %w", err)
			}
		case "type":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return fmt.Errorf("type: %w", err)
			}
			s.Type = Type(v)
		case "source":
			if err := json.Unmarshal(value, &s.Source); err != nil {
				return fmt.Errorf("source: %w", err)
			}
		case "service":
			if err := json.Unmarshal(value, &s.Service); err != nil {
				return fmt.Errorf("service: %w", err)
			}
		case "env":
			if err := json.Unmarshal(value, &s.Env); err != nil {
				return fmt.Errorf("env: %w", err)
			}
		case "severity":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return fmt.Errorf("severity: %w", err)
			}
			s.Severity = Severity(v)
		case "reason":
			if err := json.Unmarshal(value, &s.Reason); err != nil {
				return fmt.Errorf("reason: %w", err)
			}
		case "message":
			if err := json.Unmarshal(value, &s.Message); err != nil {
				return fmt.Errorf("message: %w", err)
			}
		case "resource":
			resource, err := decodeObject(value)
			if err != nil {
				return invalidField("resource", "resource must be an object")
			}
			s.Resource = resource
		case "metadata":
			metadata, err := decodeObject(value)
			if err != nil {
				return invalidField("metadata", "metadata must be an object")
			}
			s.Metadata = metadata
		case "timestamp":
			if err := json.Unmarshal(value, &s.Timestamp); err != nil {
				return fmt.Errorf("timestamp: %w", err)
			}
		case "received_at":
			if err := json.Unmarshal(value, &s.ReceivedAt); err != nil {
				return fmt.Errorf("received_at: %w", err)
			}
		default:
			var v any
			if err := json.Unmarshal(value, &v); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			extra[key] = v
		}
	}
	if len(extra) > 0 {
		s.Extra = extra
	}
	return nil
}

func (s Signal) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for key, value := range s.Extra {
		out[key] = value
	}
	out["signal_id"] = s.SignalID
	out["type"] = s.Type
	out["source"] = s.Source
	out["service"] = s.Service
	out["env"] = s.Env
	out["severity"] = s.Severity
	out["reason"] = s.Reason
	if s.Message != "" {
		out["message"] = s.Message
	}
	if s.Resource != nil {
		out["resource"] = s.Resource
	}
	if s.Metadata != nil {
		out["metadata"] = s.Metadata
	}
	if !s.Timestamp.IsZero() {
		out["timestamp"] = s.Timestamp
	}
	if !s.ReceivedAt.IsZero() {
		out["received_at"] = s.ReceivedAt
	}
	return json.Marshal(out)
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("must be an object")
	}
	return out, nil
}
