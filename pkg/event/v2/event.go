// Package eventv2 defines the Waylog v2.0 wide-event schema, types, and
// JSON Schema validation helpers.
package eventv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const SchemaVersion2 = "2.0"

type Status string

const (
	StatusOK         Status = "ok"
	StatusError      Status = "error"
	StatusTimeout    Status = "timeout"
	StatusPartial    Status = "partial"
	StatusAborted    Status = "aborted"
	StatusSuppressed Status = "suppressed"
)

func (s Status) IsPriority() bool {
	switch s {
	case StatusError, StatusTimeout, StatusPartial, StatusAborted:
		return true
	default:
		return false
	}
}

const (
	CodeTimeout = "WAYLOG_TIMEOUT"
	CodeAborted = "WAYLOG_ABORTED"
	CodePanic   = "WAYLOG_PANIC"
	CodePartial = "WAYLOG_PARTIAL"
)

type Event struct {
	SchemaVersion string         `json:"schema_version"`
	EventID       string         `json:"event_id"`
	TsStart       time.Time      `json:"ts_start"`
	TsEnd         time.Time      `json:"ts_end"`
	DurationMS    int64          `json:"duration_ms"`
	Kind          string         `json:"kind"`
	Service       string         `json:"service"`
	Env           string         `json:"env"`
	Version       string         `json:"version,omitempty"`
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id"`
	Status        Status         `json:"status"`
	Anchor        *Anchor        `json:"anchor,omitempty"`
	Steps         []Step         `json:"steps,omitempty"`
	Logs          []Log          `json:"logs,omitempty"`
	Fields        map[string]any `json:"fields,omitempty"`
	Errors        []ErrorRef     `json:"errors,omitempty"`
}

type Anchor struct {
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
	Kind      string `json:"kind,omitempty"`
}

type Step struct {
	Name       string      `json:"name"`
	SpanID     string      `json:"span_id,omitempty"`
	StartMS    int64       `json:"start_ms"`
	DurationMS int64       `json:"duration_ms"`
	Status     string      `json:"status"` // schema-restricted to "ok" or "error"
	Downstream *Downstream `json:"downstream,omitempty"`
	Error      *StepError  `json:"error,omitempty"`
}

type Downstream struct {
	Service  string `json:"service,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type StepError struct {
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	Cause  string `json:"cause,omitempty"`
}

type Log struct {
	TsOffsetMS int64          `json:"ts_offset_ms"`
	Level      string         `json:"level"`
	Msg        string         `json:"msg"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type ErrorRef struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

// Validate checks an in-memory Event against the v2.0 JSON Schema at schemaPath.
// Callers in hot paths should prefer CompileSchema once + ValidateAny per event.
func Validate(schemaPath string, e *Event) error {
	sch, err := CompileSchema(schemaPath)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return ValidateAny(sch, v)
}

// ValidateFile reads a JSON file from disk and validates it against schemaPath.
func ValidateFile(schemaPath, eventPath string) error {
	sch, err := CompileSchema(schemaPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		return fmt.Errorf("read event: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("parse event: %w", err)
	}
	return ValidateAny(sch, v)
}

// ValidateAny runs a previously-compiled schema against an arbitrary decoded
// JSON value (typically map[string]any from json.Unmarshal). Validating the
// raw decoded shape — rather than a typed struct — avoids zero-value masking
// of missing required fields.
func ValidateAny(schema *jsonschema.Schema, v any) error {
	return schema.Validate(v)
}

const schemaResourceID = "schema"

// CompileSchema reads and compiles a JSON Schema document at schemaPath.
// The returned *jsonschema.Schema is safe for concurrent reuse across many
// ValidateAny calls; callers should compile once at startup.
func CompileSchema(schemaPath string) (*jsonschema.Schema, error) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	return compileFromBytes(raw)
}

func compileFromBytes(raw []byte) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaResourceID, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}
	sch, err := c.Compile(schemaResourceID)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return sch, nil
}
