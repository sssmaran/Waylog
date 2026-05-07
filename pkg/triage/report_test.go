package triage_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestReportJSONRoundTrip(t *testing.T) {
	in := triage.Report{
		SchemaVersion: "triage.v1",
		IncidentRef:   triage.IncidentRef{ID: "inc_test", Window: "15m"},
		BlastSnapshot: triage.BlastSnapshot{
			Requests: 12, Users: 8, Services: 4,
			TopErrorFamilies: []triage.ErrorFamily{
				{Service: "payment", Step: "payment.charge", ErrorCode: "PMT_502", Count: 11},
			},
		},
		Signals:     []triage.SignalRef{{ID: "sig_1", Type: "deploy", EvidenceIDs: []string{"e1"}}},
		NextChecks:  []triage.NextCheck{{ID: "check_1", Prompt: "verify x"}},
		Confidence:  triage.ConfidenceMedium,
		GeneratedAt: "2026-05-06T00:00:00Z",
		ReportHash:  "sha256:abc",
	}
	raw, err := json.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out triage.Report
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SchemaVersion != in.SchemaVersion {
		t.Fatalf("schema_version mismatch: got %q want %q", out.SchemaVersion, in.SchemaVersion)
	}
	if out.BlastSnapshot.TopErrorFamilies[0].ErrorCode != "PMT_502" {
		t.Fatalf("top_error_families round-trip lost data: %+v", out.BlastSnapshot.TopErrorFamilies)
	}
	if out.Confidence != triage.ConfidenceMedium {
		t.Fatalf("confidence mismatch: got %q", out.Confidence)
	}
}

func TestReportValidate(t *testing.T) {
	good := triage.Report{
		SchemaVersion: "triage.v1",
		IncidentRef:   triage.IncidentRef{ID: "inc_x"},
		Confidence:    triage.ConfidenceMedium,
		GeneratedAt:   "2026-05-06T00:00:00Z",
		ReportHash:    "sha256:x",
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good report failed validation: %v", err)
	}

	cases := map[string]triage.Report{
		"missing schema_version": {IncidentRef: triage.IncidentRef{ID: "inc_x"}, Confidence: triage.ConfidenceLow, GeneratedAt: "t", ReportHash: "h"},
		"wrong schema_version":   {SchemaVersion: "triage.v2", IncidentRef: triage.IncidentRef{ID: "inc_x"}, Confidence: triage.ConfidenceLow, GeneratedAt: "t", ReportHash: "h"},
		"missing incident id":    {SchemaVersion: "triage.v1", Confidence: triage.ConfidenceLow, GeneratedAt: "t", ReportHash: "h"},
		"bad confidence":         {SchemaVersion: "triage.v1", IncidentRef: triage.IncidentRef{ID: "inc_x"}, Confidence: "extreme", GeneratedAt: "t", ReportHash: "h"},
		"missing generated_at":   {SchemaVersion: "triage.v1", IncidentRef: triage.IncidentRef{ID: "inc_x"}, Confidence: triage.ConfidenceLow, ReportHash: "h"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if err := r.Validate(); err == nil {
				t.Fatalf("%s: expected validation error, got nil", name)
			}
		})
	}
}

func TestCanonicalHashExcludesGeneratedAtPlanRunIDAndReportHash(t *testing.T) {
	a := triage.Report{
		SchemaVersion: "triage.v1",
		IncidentRef:   triage.IncidentRef{ID: "inc_1"},
		Confidence:    triage.ConfidenceMedium,
		GeneratedAt:   "2026-05-06T00:00:00Z",
		ReportHash:    "sha256:placeholder",
	}
	hashA, err := a.CanonicalHash()
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}

	b := a
	b.GeneratedAt = "2099-01-01T00:00:00Z"
	b.PlanRunID = "plan_other"
	b.ReportHash = "sha256:something_else"
	hashB, err := b.CanonicalHash()
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}

	if hashA != hashB {
		t.Fatalf("CanonicalHash must exclude generated_at, plan_run_id, report_hash. got %q vs %q", hashA, hashB)
	}
}

func TestCanonicalHashChangesWhenContentChanges(t *testing.T) {
	base := triage.Report{
		SchemaVersion: "triage.v1",
		IncidentRef:   triage.IncidentRef{ID: "inc_1"},
		Confidence:    triage.ConfidenceMedium,
		GeneratedAt:   "t",
		ReportHash:    "h",
	}
	h1, _ := base.CanonicalHash()

	mutated := base
	mutated.IncidentRef.ID = "inc_2"
	h2, _ := mutated.CanonicalHash()
	if h1 == h2 {
		t.Fatalf("hash must change when incident_ref.id changes")
	}
}

func TestCanonicalHashFormat(t *testing.T) {
	r := triage.Report{
		SchemaVersion: "triage.v1",
		IncidentRef:   triage.IncidentRef{ID: "inc_1"},
		Confidence:    triage.ConfidenceLow,
		GeneratedAt:   "t",
		ReportHash:    "h",
	}
	h, err := r.CanonicalHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(h, "sha256:") {
		t.Fatalf("hash should be prefixed with sha256:, got %q", h)
	}
	if len(h) != len("sha256:")+64 {
		t.Fatalf("hash length wrong: got %d (%q)", len(h), h)
	}
}
