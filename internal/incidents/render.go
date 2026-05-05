package incidents

import (
	"fmt"
	"strings"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func RenderSnapshot(inc Incident) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Incident %s\n", inc.IncidentID)
	fmt.Fprintf(&b, "Status: %s\n", inc.Status)
	fmt.Fprintf(&b, "Family: %s\n", apiv2.FormatErrorFamily(inc.ErrorFamily))
	fmt.Fprintf(&b, "Cause: %s (%s confidence)\n", inc.Cause, inc.Confidence)
	fmt.Fprintf(&b, "Started: %s\n", inc.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "Affected: %d requests, %d services\n", inc.AffectedRequests, inc.AffectedServices)
	fmt.Fprintf(&b, "Lift: %.2fx over baseline %d\n", inc.Lift, inc.BaselineCount)
	if len(inc.TopServices) > 0 {
		fmt.Fprintf(&b, "Top services: %s\n", strings.Join(inc.TopServices, ", "))
	}
	if len(inc.SampleTraces) > 0 {
		fmt.Fprintf(&b, "Sample traces: %s\n", strings.Join(inc.SampleTraces, ", "))
	}
	if len(inc.Evidence) > 0 {
		b.WriteString("\nEvidence:\n")
		for _, ev := range inc.Evidence {
			fmt.Fprintf(&b, "- %s: %s", ev.Kind, ev.Title)
			if ev.Detail != "" {
				fmt.Fprintf(&b, " (%s)", ev.Detail)
			}
			b.WriteByte('\n')
		}
	}
	if len(inc.NextChecks) > 0 {
		b.WriteString("\nNext checks:\n")
		for _, check := range inc.NextChecks {
			fmt.Fprintf(&b, "- %s\n", check)
		}
	}
	if len(inc.InstrumentationWarnings) > 0 {
		b.WriteString("\nInstrumentation warnings:\n")
		for _, warning := range inc.InstrumentationWarnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	return b.String()
}
