package signals

import (
	"errors"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	valid := Signal{
		Type:      TypeDeploy,
		Source:    "github",
		Service:   "checkout",
		Env:       "prod",
		Severity:  SeverityInfo,
		Reason:    "RolloutComplete",
		Timestamp: now,
	}
	tests := []struct {
		name string
		edit func(*Signal)
		code string
	}{
		{name: "valid"},
		{name: "missing service", edit: func(s *Signal) { s.Service = "" }, code: CodeInvalidField},
		{name: "unknown type", edit: func(s *Signal) { s.Type = "wrong" }, code: CodeUnknownType},
		{name: "unknown severity", edit: func(s *Signal) { s.Severity = "huge" }, code: CodeUnknownSeverity},
		{name: "future timestamp", edit: func(s *Signal) { s.Timestamp = now.Add(2 * time.Hour) }, code: CodeTimestampTooFarInFuture},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := valid
			if tt.edit != nil {
				tt.edit(&sig)
			}
			err := Validate(&sig, now, 5*time.Minute)
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("err=%T %[1]v", err)
			}
			if validation.Code != tt.code {
				t.Fatalf("code=%q want %q", validation.Code, tt.code)
			}
		})
	}
}
