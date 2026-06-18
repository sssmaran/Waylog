package reports

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

const (
	FormatMarkdown  = "markdown"
	FormatSlack     = "slack"
	FormatPagerDuty = "pagerduty"
)

type Rendered struct {
	Format      string `json:"format"`
	ContentType string `json:"content_type"`
	Body        any    `json:"body"`
}

func Render(rep *pkgtriage.Report, format string) (Rendered, error) {
	if rep == nil {
		return Rendered{}, fmt.Errorf("report required")
	}
	if format == "" {
		format = FormatMarkdown
	}
	switch format {
	case FormatMarkdown:
		return Rendered{Format: format, ContentType: "text/markdown", Body: Markdown(rep)}, nil
	case FormatSlack:
		return Rendered{Format: format, ContentType: "application/json", Body: Slack(rep)}, nil
	case FormatPagerDuty:
		return Rendered{Format: format, ContentType: "text/plain", Body: PagerDuty(rep)}, nil
	default:
		return Rendered{}, fmt.Errorf("unsupported report format %q", format)
	}
}

func Markdown(rep *pkgtriage.Report) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Waylog Operator Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- Incident: `%s` (report `%s`)\n", nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	fmt.Fprintf(&b, "- Evidence fingerprint: `%s` (stable across ticks until evidence changes)\n", nz(rep.EvidenceFingerprint))
	fmt.Fprintf(&b, "- Confidence: `%s` (incident `%s`, report `%s`)\n", nz(string(rep.Confidence)), nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	fmt.Fprintf(&b, "- Evidence status: alert=%s trace=%s signal=%s runtime=%s (report `%s`)\n", availability(len(rep.Alerts) > 0), availability(len(rep.SampleTraces) > 0), availability(len(rep.Signals) > 0), availability(len(rep.Runtime) > 0), nz(rep.ReportHash))
	fmt.Fprintf(&b, "- Window: `%s` (incident `%s`)\n\n", nz(rep.IncidentRef.Window), nz(rep.IncidentRef.ID))

	fmt.Fprintf(&b, "## Impact\n\n")
	fmt.Fprintf(&b, "- Requests: %d (incident `%s`, report `%s`)\n", rep.BlastSnapshot.Requests, nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	fmt.Fprintf(&b, "- Users: %d (incident `%s`, report `%s`)\n", rep.BlastSnapshot.Users, nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	fmt.Fprintf(&b, "- Services: %d (incident `%s`, report `%s`)\n", rep.BlastSnapshot.Services, nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	for _, f := range rep.BlastSnapshot.TopErrorFamilies {
		fmt.Fprintf(&b, "- Error family: `%s/%s/%s` count=%d (incident `%s`, report `%s`)\n", nz(f.Service), nz(f.Step), nz(f.ErrorCode), f.Count, nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	}
	if len(rep.BlastSnapshot.TopErrorFamilies) == 0 {
		fmt.Fprintf(&b, "- Error family: not available (incident `%s`)\n", nz(rep.IncidentRef.ID))
	}

	fmt.Fprintf(&b, "\n## First Failure And Traces\n\n")
	if len(rep.SampleTraces) == 0 {
		fmt.Fprintf(&b, "- not available (incident `%s`, report `%s`)\n", nz(rep.IncidentRef.ID), nz(rep.ReportHash))
	} else {
		for _, t := range rep.SampleTraces {
			fmt.Fprintf(&b, "- trace `%s`: %s (incident `%s`, report `%s`)\n", nz(t.TraceID), nz(t.Summary), nz(rep.IncidentRef.ID), nz(rep.ReportHash))
		}
	}

	fmt.Fprintf(&b, "\n## Alert Evidence\n\n")
	if len(rep.Alerts) == 0 {
		fmt.Fprintf(&b, "- not available (report `%s`)\n", nz(rep.ReportHash))
	} else {
		for _, a := range rep.Alerts {
			fmt.Fprintf(&b, "- `%s` from `%s`: %s; provider=%s (signal `%s`, alert `%s`, report `%s`)\n", nz(a.Severity), nz(a.Source), nz(a.Reason), nz(a.ProviderURL), nz(a.SignalID), nz(a.AlertID), nz(rep.ReportHash))
		}
	}

	fmt.Fprintf(&b, "\n## Runtime Evidence\n\n")
	if len(rep.Runtime) == 0 {
		fmt.Fprintf(&b, "- not available (report `%s`)\n", nz(rep.ReportHash))
	} else {
		for _, r := range rep.Runtime {
			fmt.Fprintf(&b, "- `%s` %s on `%s`: %s (source `%s`, signal `%s`, report `%s`)\n", nz(r.Severity), nz(r.Subtype), nz(r.Service), nz(r.Reason), nz(r.Source), nz(r.SignalID), nz(rep.ReportHash))
		}
	}

	fmt.Fprintf(&b, "\n## Signals\n\n")
	if len(rep.Signals) == 0 {
		fmt.Fprintf(&b, "- not available (report `%s`)\n", nz(rep.ReportHash))
	} else {
		for _, s := range rep.Signals {
			fmt.Fprintf(&b, "- `%s` signal `%s` evidence=%s (report `%s`)\n", nz(s.Type), nz(s.ID), strings.Join(s.EvidenceIDs, ","), nz(rep.ReportHash))
		}
	}

	fmt.Fprintf(&b, "\n## Next Checks\n\n")
	if len(rep.NextChecks) == 0 {
		fmt.Fprintf(&b, "- not available (report `%s`)\n", nz(rep.ReportHash))
	} else {
		for _, c := range rep.NextChecks {
			fmt.Fprintf(&b, "- %s (check `%s`, report `%s`)\n", nz(c.Prompt), nz(c.ID), nz(rep.ReportHash))
		}
	}
	return b.String()
}

func Slack(rep *pkgtriage.Report) map[string]any {
	fields := []map[string]string{
		{"type": "mrkdwn", "text": "*Incident*\n`" + nz(rep.IncidentRef.ID) + "`"},
		{"type": "mrkdwn", "text": "*Confidence*\n`" + nz(string(rep.Confidence)) + "` (incident `" + nz(rep.IncidentRef.ID) + "`, report `" + nz(rep.ReportHash) + "`)"},
		{"type": "mrkdwn", "text": "*Impact*\n" + impactSummary(rep)},
		{"type": "mrkdwn", "text": "*Trace*\n" + firstTrace(rep)},
		{"type": "mrkdwn", "text": "*Report hash*\n`" + nz(rep.ReportHash) + "`"},
		{"type": "mrkdwn", "text": "*Evidence fingerprint*\n`" + nz(rep.EvidenceFingerprint) + "`"},
	}
	alertText := "not available"
	if len(rep.Alerts) > 0 {
		a := rep.Alerts[0]
		alertText = fmt.Sprintf("`%s` %s provider=%s (signal `%s`, alert `%s`, report `%s`)", nz(a.Source), nz(a.Reason), nz(a.ProviderURL), nz(a.SignalID), nz(a.AlertID), nz(rep.ReportHash))
	}
	return map[string]any{
		"blocks": []map[string]any{
			{"type": "header", "text": map[string]string{"type": "plain_text", "text": "Waylog operator report"}},
			{"type": "section", "fields": fields},
			{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*Alert evidence*\n" + alertText}},
			{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*Runtime evidence*\n" + runtimeSummary(rep)}},
			{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*Next check*\n" + firstCheck(rep)}},
		},
	}
}

func PagerDuty(rep *pkgtriage.Report) string {
	alert := "not available"
	if len(rep.Alerts) > 0 {
		a := rep.Alerts[0]
		alert = fmt.Sprintf("%s alert %s via signal %s provider=%s", nz(a.Source), nz(a.AlertID), nz(a.SignalID), nz(a.ProviderURL))
	}
	return fmt.Sprintf("Waylog operator report: incident=%s confidence=%s impact=%s trace=%s report_hash=%s evidence_fingerprint=%s alert=%s runtime=%s next_check=%s",
		nz(rep.IncidentRef.ID), nz(string(rep.Confidence)), impactSummary(rep), firstTrace(rep), nz(rep.ReportHash), nz(rep.EvidenceFingerprint), alert, runtimeSummary(rep), firstCheck(rep))
}

func EncodeBody(r Rendered) ([]byte, error) {
	if r.Format == FormatSlack {
		return json.MarshalIndent(r.Body, "", "  ")
	}
	if s, ok := r.Body.(string); ok {
		return []byte(s), nil
	}
	return json.MarshalIndent(r.Body, "", "  ")
}

func firstCheck(rep *pkgtriage.Report) string {
	if len(rep.NextChecks) == 0 {
		return "not available (report `" + nz(rep.ReportHash) + "`)"
	}
	return nz(rep.NextChecks[0].Prompt) + " (check `" + nz(rep.NextChecks[0].ID) + "`, report `" + nz(rep.ReportHash) + "`)"
}

func firstTrace(rep *pkgtriage.Report) string {
	if len(rep.SampleTraces) == 0 {
		return "not available (report `" + nz(rep.ReportHash) + "`)"
	}
	return "`" + nz(rep.SampleTraces[0].TraceID) + "` (incident `" + nz(rep.IncidentRef.ID) + "`, report `" + nz(rep.ReportHash) + "`)"
}

// runtimeSummary renders matched runtime evidence as a compact list: infra and
// app rows count toward the same incident. Used by Slack and PagerDuty.
func runtimeSummary(rep *pkgtriage.Report) string {
	if len(rep.Runtime) == 0 {
		return "not available (report `" + nz(rep.ReportHash) + "`)"
	}
	parts := make([]string, 0, len(rep.Runtime))
	for _, r := range rep.Runtime {
		parts = append(parts, fmt.Sprintf("`%s` %s on %s (%s)", nz(r.Severity), nz(r.Subtype), nz(r.Service), nz(r.Source)))
	}
	return strings.Join(parts, "; ") + " (report `" + nz(rep.ReportHash) + "`)"
}

func impactSummary(rep *pkgtriage.Report) string {
	return fmt.Sprintf("%d requests, %d users, %d services (incident `%s`, report `%s`)",
		rep.BlastSnapshot.Requests, rep.BlastSnapshot.Users, rep.BlastSnapshot.Services, nz(rep.IncidentRef.ID), nz(rep.ReportHash))
}

func availability(ok bool) string {
	if ok {
		return "present"
	}
	return "not available"
}

func nz(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not available"
	}
	return s
}
