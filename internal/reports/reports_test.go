package reports

import (
	"encoding/json"
	"strings"
	"testing"

	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestMarkdownReportCitesEvidence(t *testing.T) {
	out := Markdown(testReport())
	for _, want := range []string{"Requests: 12 (incident `inc_abc`, report `sha256:test`)", "trace_1", "sig_alert", "alert_1", "check_0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown missing %q:\n%s", want, out)
		}
	}
}

func TestSlackReportIsJSONAndCitesEvidence(t *testing.T) {
	rendered, err := Render(testReport(), FormatSlack)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeBody(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid json: %s", raw)
	}
	for _, want := range []string{"sig_alert", "alert_1", "sha256:test"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("slack payload missing %q:\n%s", want, raw)
		}
	}
}

func TestPagerDutyReportCitesEvidence(t *testing.T) {
	out := PagerDuty(testReport())
	for _, want := range []string{"inc_abc", "sig_alert", "alert_1", "sha256:test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pagerduty missing %q:\n%s", want, out)
		}
	}
}

func testReport() *pkgtriage.Report {
	return &pkgtriage.Report{
		SchemaVersion: pkgtriage.SchemaVersionV1,
		IncidentRef:   pkgtriage.IncidentRef{ID: "inc_abc", Window: "15m"},
		BlastSnapshot: pkgtriage.BlastSnapshot{
			Requests: 12,
			Users:    2,
			Services: 3,
			TopErrorFamilies: []pkgtriage.ErrorFamily{
				{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502", Count: 12},
			},
		},
		SampleTraces: []pkgtriage.TraceSample{{TraceID: "trace_1", Summary: "checkout payment failure"}},
		Signals:      []pkgtriage.SignalRef{{ID: "sig_alert", Type: "alert", EvidenceIDs: []string{"sig_alert"}}},
		Alerts:       []pkgtriage.AlertRef{{SignalID: "sig_alert", AlertID: "alert_1", Source: "grafana", Severity: "critical", Reason: "PMT_502 spike", EvidenceIDs: []string{"sig_alert"}}},
		NextChecks:   []pkgtriage.NextCheck{{ID: "check_0", Prompt: "Check payment health"}},
		Confidence:   pkgtriage.ConfidenceHigh,
		GeneratedAt:  "2026-05-10T12:00:00Z",
		ReportHash:   "sha256:test",
	}
}
