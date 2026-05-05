package signals

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSignalJSONPreservesExtraAndOverridesServerFields(t *testing.T) {
	raw := []byte(`{
		"signal_id":"client",
		"type":"deploy",
		"source":"github",
		"service":"checkout",
		"env":"prod",
		"severity":"info",
		"reason":"RolloutComplete",
		"metadata":{"version":"1.2.3"},
		"timestamp":"2026-05-02T18:09:40Z",
		"received_at":"2026-05-02T18:09:41Z",
		"custom_tag":"foo"
	}`)
	var sig Signal
	if err := json.Unmarshal(raw, &sig); err != nil {
		t.Fatal(err)
	}
	if got := sig.Extra["custom_tag"]; got != "foo" {
		t.Fatalf("custom_tag=%v", got)
	}
	sig.SignalID = "sig_server"
	sig.ReceivedAt = time.Date(2026, 5, 2, 18, 9, 42, 0, time.UTC)
	out, err := json.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["signal_id"] != "sig_server" {
		t.Fatalf("signal_id=%v", decoded["signal_id"])
	}
	if decoded["custom_tag"] != "foo" {
		t.Fatalf("custom_tag=%v", decoded["custom_tag"])
	}
}

func TestTypeAndSeverityValidity(t *testing.T) {
	for _, typ := range []Type{TypeDeploy, TypeRuntime, TypeHealthcheck, TypeDependency, TypeConfig, TypeAlert} {
		if !typ.Valid() {
			t.Fatalf("%q should be valid", typ)
		}
	}
	if Type("bad").Valid() {
		t.Fatal("bad type should be invalid")
	}
	for _, severity := range []Severity{SeverityInfo, SeverityWarning, SeverityCritical} {
		if !severity.Valid() {
			t.Fatalf("%q should be valid", severity)
		}
	}
	if Severity("huge").Valid() {
		t.Fatal("bad severity should be invalid")
	}
}

func TestSignalJSONRejectsNonObjectResource(t *testing.T) {
	var sig Signal
	err := json.Unmarshal([]byte(`{"resource":"bad"}`), &sig)
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Code != CodeInvalidField {
		t.Fatalf("err=%T %[1]v", err)
	}
}
