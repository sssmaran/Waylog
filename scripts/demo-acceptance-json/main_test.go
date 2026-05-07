package main

import "testing"

func TestBurstSignalsAccepted(t *testing.T) {
	body := []byte(`{"signals":[
		{"type":"deploy","service":"checkout","reason":"demo_checkout_rollout","accepted":true},
		{"type":"dependency","service":"payment","reason":"payment_gateway_5xx","accepted":true}
	]}`)
	if !burstSignalsAccepted(body) {
		t.Fatal("expected accepted deploy and dependency signals")
	}
}

func TestDependencyIncidentHelpers(t *testing.T) {
	body := []byte(`{"incidents":[{"incident_id":"inc_123","status":"active","cause":"dependency","confidence":"high","error_family":{"service":"checkout","step":"payment.charge","error_code":"PMT_502"}}]}`)
	if !hasDependencyIncident(body) {
		t.Fatal("expected dependency incident")
	}
	if got := firstIncidentID(body); got != "inc_123" {
		t.Fatalf("firstIncidentID = %q, want inc_123", got)
	}
}

func TestTriageReportHash(t *testing.T) {
	body := []byte(`{"schema_version":"triage.v1","incident_ref":{"id":"inc_x"},"confidence":"medium","generated_at":"t","report_hash":"sha256:deadbeef"}`)
	if got := triageReportHash(body); got != "sha256:deadbeef" {
		t.Fatalf("triageReportHash = %q, want sha256:deadbeef", got)
	}
	if got := triageReportHash([]byte(`{not-json`)); got != "" {
		t.Fatalf("malformed input should return empty, got %q", got)
	}
}
