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

func TestActiveIncidentIDs(t *testing.T) {
	body := []byte(`{"incidents":[
		{"incident_id":"inc_a","status":"active"},
		{"incident_id":"inc_b","status":"resolved"},
		{"incident_id":"inc_c","status":"active"},
		{"incident_id":"","status":"active"}
	]}`)
	got := activeIncidentIDs(body)
	want := []string{"inc_a", "inc_c"}
	if len(got) != len(want) {
		t.Fatalf("activeIncidentIDs len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("activeIncidentIDs[%d] = %q, want %q", i, got[i], id)
		}
	}
	if got := activeIncidentIDs([]byte(`{not-json`)); got != nil {
		t.Fatalf("malformed input should return nil, got %v", got)
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

func TestPlanTriageReportHash(t *testing.T) {
	body := []byte(`{"steps":[{"result":{"schema_version":"triage.v1","report_hash":"sha256:plan"}}]}`)
	if got := planTriageReportHash(body); got != "sha256:plan" {
		t.Fatalf("planTriageReportHash = %q, want sha256:plan", got)
	}
	if got := planTriageReportHash([]byte(`{"steps":[]}`)); got != "" {
		t.Fatalf("missing plan result should return empty, got %q", got)
	}
}

func TestTriageEvidenceHelpers(t *testing.T) {
	body := []byte(`{
		"blast_snapshot":{"top_error_families":[{"service":"checkout","step":"payment.charge","error_code":"PMT_502"}]},
		"sample_traces":[{"trace_id":"trace_1"}],
		"signals":[{"id":"sig_dep","type":"dependency"}],
		"alerts":[{"signal_id":"sig_alert","alert_id":"alert_1"}],
		"next_checks":[{"id":"check_0","prompt":"Check payment health"}]
	}`)
	if !triageRootCauseAccurate(body) || !triageHasTrace(body) || !triageHasDependencySignal(body) || !triageHasAlert(body) || !triageHasNextCheck(body) {
		t.Fatalf("expected all triage helpers to pass")
	}
	if !triageHasAlertID(body, "alert_1") {
		t.Fatalf("expected alert_1 to be present")
	}
	if triageHasAlertID(body, "alert_other") {
		t.Fatalf("unexpected alert_other match")
	}
	if triageHasAlert([]byte(`{"alerts":[]}`)) {
		t.Fatalf("empty alerts should fail")
	}
}

func TestIncidentCauseIsDependency(t *testing.T) {
	body := []byte(`{"incidents":[
		{"incident_id":"inc_a","cause":"app"},
		{"incident_id":"inc_b","cause":"dependency"}
	]}`)
	if !incidentCauseIsDependency(body, "inc_b") {
		t.Fatalf("expected inc_b to be dependency")
	}
	if incidentCauseIsDependency(body, "inc_a") {
		t.Fatalf("inc_a should not be dependency")
	}
}
