package cliv2

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func renderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func RenderErrors(w io.Writer, resp ErrorsResponse) {
	if len(resp.Rows) == 0 {
		fmt.Fprintln(w, "No error families found.")
		renderNextCursor(w, resp.NextCursor)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ERROR FAMILY\tCOUNT\tAFFECTED TRACES\tAFFECTED USERS\tSAMPLE")
	for _, row := range resp.Rows {
		users := "null"
		if row.AffectedUsers != nil {
			users = fmt.Sprintf("%d", *row.AffectedUsers)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\n",
			apiv2.FormatErrorFamily(row.ErrorFamily),
			row.Count,
			row.AffectedTraces,
			users,
			truncateList(row.SampleTraces),
		)
	}
	_ = tw.Flush()
	renderNextCursor(w, resp.NextCursor)
}

func RenderRecent(w io.Writer, resp RecentTracesResponse) {
	if len(resp.Traces) == 0 {
		fmt.Fprintln(w, "No recent traces found.")
		renderNextCursor(w, resp.NextCursor)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TRACE\tSTATUS\tSERVICES\tDURATION\tANCHOR\tSTART")
	for _, trace := range resp.Traces {
		anchor := ""
		if trace.AnchorSummary != nil {
			anchor = *trace.AnchorSummary
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d ms\t%s\t%s\n",
			truncateID(trace.TraceID),
			trace.Status,
			strings.Join(trace.Services, " -> "),
			trace.DurationMS,
			anchor,
			formatTime(trace.TsStart),
		)
	}
	_ = tw.Flush()
	renderNextCursor(w, resp.NextCursor)
}

func RenderIncidents(w io.Writer, resp IncidentListResponse) {
	if len(resp.Incidents) == 0 {
		fmt.Fprintln(w, "No active incidents.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "INCIDENT\tSTATUS\tCAUSE\tCONF\tSEVERITY\tFAMILY\tAFFECTED\tSTARTED")
	for _, inc := range resp.Incidents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%d req / %d svc\t%s\n",
			truncateID(inc.IncidentID),
			inc.Status,
			inc.Cause,
			inc.Confidence,
			inc.Severity,
			apiv2.FormatErrorFamily(inc.ErrorFamily),
			inc.AffectedRequests,
			inc.AffectedServices,
			formatTime(inc.StartedAt),
		)
	}
	_ = tw.Flush()
}

func RenderIncident(w io.Writer, resp IncidentDetailResponse) {
	renderIncidentBody(w, resp.Incident)
}

func RenderIncidentSnapshot(w io.Writer, resp IncidentSnapshotResponse) {
	if resp.Snapshot != "" {
		fmt.Fprintln(w, resp.Snapshot)
	}
}

func renderIncidentBody(w io.Writer, inc Incident) {
	fmt.Fprintf(w, "incident_id: %s\n", inc.IncidentID)
	fmt.Fprintf(w, "status: %s\n", inc.Status)
	fmt.Fprintf(w, "family: %s\n", apiv2.FormatErrorFamily(inc.ErrorFamily))
	fmt.Fprintf(w, "cause: %s (%s confidence)\n", inc.Cause, inc.Confidence)
	fmt.Fprintf(w, "severity: %d\n", inc.Severity)
	fmt.Fprintf(w, "started_at: %s\n", formatTime(inc.StartedAt))
	fmt.Fprintf(w, "updated_at: %s\n", formatTime(inc.UpdatedAt))
	if inc.ResolvedAt != nil {
		fmt.Fprintf(w, "resolved_at: %s\n", formatTime(*inc.ResolvedAt))
	}
	fmt.Fprintf(w, "affected_requests: %d\n", inc.AffectedRequests)
	if inc.AffectedUsers == nil {
		fmt.Fprintln(w, "affected_users: null")
	} else {
		fmt.Fprintf(w, "affected_users: %d\n", *inc.AffectedUsers)
	}
	fmt.Fprintf(w, "affected_services: %d\n", inc.AffectedServices)
	fmt.Fprintf(w, "top_services: %s\n", strings.Join(inc.TopServices, ","))
	fmt.Fprintf(w, "lift: %.2f\n", inc.Lift)
	fmt.Fprintf(w, "baseline_count: %d\n", inc.BaselineCount)
	fmt.Fprintf(w, "current_count: %d\n", inc.CurrentCount)

	renderPropagationBlock(w, inc.Propagation)
	renderBlastBlock(w, inc.Blast)

	fmt.Fprintln(w, "\nevidence:")
	if len(inc.Evidence) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		for _, ev := range inc.Evidence {
			detail := ev.Detail
			if detail == "" {
				detail = ev.Service
			}
			fmt.Fprintf(w, "  - %s: %s", ev.Kind, ev.Title)
			if detail != "" {
				fmt.Fprintf(w, " (%s)", detail)
			}
			if ev.TraceID != "" {
				fmt.Fprintf(w, " trace=%s", truncateID(ev.TraceID))
			}
			fmt.Fprintln(w)
		}
	}

	fmt.Fprintln(w, "\nnext_checks:")
	if len(inc.NextChecks) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		for _, check := range inc.NextChecks {
			fmt.Fprintf(w, "  - %s\n", check)
		}
	}

	if len(inc.InstrumentationWarnings) > 0 {
		fmt.Fprintln(w, "\ninstrumentation_warnings:")
		for _, warning := range inc.InstrumentationWarnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
	if len(inc.SampleTraces) > 0 {
		fmt.Fprintf(w, "\nsample_traces: %s\n", truncateList(inc.SampleTraces))
	}
}

func RenderEvent(w io.Writer, ev *Event) {
	if ev == nil {
		fmt.Fprintln(w, "No event found.")
		return
	}
	fmt.Fprintf(w, "event_id: %s\n", ev.EventID)
	fmt.Fprintf(w, "trace_id: %s\n", ev.TraceID)
	fmt.Fprintf(w, "service: %s\n", ev.Service)
	fmt.Fprintf(w, "status: %s\n", ev.Status)
	fmt.Fprintf(w, "duration_ms: %d\n", ev.DurationMS)
	if route := eventRoute(ev); route != "" {
		fmt.Fprintf(w, "route: %s\n", route)
	}
	if ev.Anchor == nil {
		fmt.Fprintln(w, "anchor: none")
	} else {
		fmt.Fprintf(w, "anchor: %s -> %s\n", ev.Anchor.Step, ev.Anchor.ErrorCode)
	}
	fmt.Fprintf(w, "steps: %d\n", len(ev.Steps))
	fmt.Fprintf(w, "logs: %d\n", len(ev.Logs))
	fmt.Fprintf(w, "downstream: %d\n", countEventDownstream(ev))
}

func RenderTrace(w io.Writer, resp TraceGetResponse) {
	fmt.Fprintf(w, "trace_id: %s\n", resp.TraceID)
	fmt.Fprintf(w, "linkage: %s\n\n", resp.Linkage)
	if len(resp.Events) == 0 {
		fmt.Fprintln(w, "No events found.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT\tSTATUS\tSERVICE\tSTART")
	for _, ev := range resp.Events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", truncateID(ev.EventID), ev.Status, ev.Service, formatTime(ev.TsStart))
	}
	_ = tw.Flush()
}

func RenderStory(w io.Writer, story StoryResponse) {
	fmt.Fprintf(w, "trace_id: %s\n", story.TraceID)
	fmt.Fprintf(w, "service: %s\n", story.Service)
	if story.Route != "" {
		fmt.Fprintf(w, "route: %s\n", story.Route)
	}
	fmt.Fprintf(w, "status: %s\n", story.Status)
	fmt.Fprintf(w, "linkage: %s\n\n", story.Linkage)

	fmt.Fprintln(w, "first observable failing step:")
	if story.Anchor == nil {
		fmt.Fprintln(w, "  none observed")
	} else {
		fmt.Fprintf(w, "  %s -> %s\n", story.Anchor.Step, story.Anchor.ErrorCode)
	}

	fmt.Fprintln(w, "\ncontributing path:")
	if len(story.Path) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, step := range story.Path {
			detail := step.ErrorMsg
			if detail == "" {
				detail = step.ErrorCode
			}
			fmt.Fprintf(tw, "  %s\t%s\t%d ms\t%s\n", step.Name, step.Status, step.DurationMS, detail)
		}
		_ = tw.Flush()
	}

	if len(story.Logs) > 0 {
		fmt.Fprintln(w, "\nlogs:")
		for _, log := range story.Logs {
			fmt.Fprintf(w, "  +%d ms [%s] %s\n", log.TsOffsetMS, log.Level, log.Msg)
		}
	}

	fmt.Fprintln(w, "\ndownstream:")
	if len(story.Downstream) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	for _, downstream := range story.Downstream {
		target := downstream.Service
		if downstream.Endpoint != "" {
			target += " " + downstream.Endpoint
		}
		fmt.Fprintf(w, "  %s -> %s\n", downstream.Step, target)
	}
}

func RenderBlast(w io.Writer, resp BlastRadiusResponse) {
	fmt.Fprintf(w, "view_mode: %s\n", resp.ViewMode)
	fmt.Fprintf(w, "key: %s\n", formatBlastKey(resp.Key))
	fmt.Fprintf(w, "affected_requests: %d\n", resp.AffectedRequests)
	fmt.Fprintf(w, "affected_services: %d\n", resp.AffectedServices)
	if resp.AffectedUsers == nil {
		fmt.Fprintln(w, "affected_users: null")
	} else {
		fmt.Fprintf(w, "affected_users: %d\n", *resp.AffectedUsers)
	}
	fmt.Fprintf(w, "top_services: %s\n", strings.Join(resp.TopServices, ","))
	fmt.Fprintf(w, "sample_traces: %s\n", truncateList(resp.SampleTraces))
}

func RenderSearch(w io.Writer, resp EventSearchResponse) {
	if len(resp.Events) == 0 {
		fmt.Fprintln(w, "No events found.")
		renderNextCursor(w, resp.NextCursor)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT\tSTATUS\tSERVICE\tTRACE\tSTART")
	for _, ev := range resp.Events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", truncateID(ev.EventID), ev.Status, ev.Service, truncateID(ev.TraceID), formatTime(ev.TsStart))
	}
	_ = tw.Flush()
	renderNextCursor(w, resp.NextCursor)
}

func RenderCapabilities(w io.Writer, resp CapabilitiesResponse) {
	fmt.Fprintf(w, "otlp_http_traces: %s\n", enabledLabel(resp.OTLP.HTTPTraces))
	fmt.Fprintf(w, "otlp_grpc_traces: %s", enabledLabel(resp.OTLP.GRPCTraces))
	if resp.OTLP.GRPCAddr != "" {
		fmt.Fprintf(w, " addr=%s", resp.OTLP.GRPCAddr)
	}
	fmt.Fprintln(w)
	if resp.LLM.Provider != "" {
		fmt.Fprintf(w, "llm: provider=%s configured=%t ask_enabled=%t", resp.LLM.Provider, resp.LLM.Configured, resp.LLM.AskEnabled)
		if resp.LLM.Model != "" {
			fmt.Fprintf(w, " model=%s", resp.LLM.Model)
		}
		if resp.LLM.ToolMode != "" {
			fmt.Fprintf(w, " tool_mode=%s", resp.LLM.ToolMode)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "incidents: enabled=%t persistent=%t rebuild_supported=%t",
		resp.Incidents.Enabled, resp.Incidents.Persistent, resp.Incidents.Rebuild.Supported)
	if resp.Incidents.Rebuild.Scope != "" {
		fmt.Fprintf(w, " rebuild_scope=%s", resp.Incidents.Rebuild.Scope)
	}
	fmt.Fprintln(w)
}

func eventRoute(ev *Event) string {
	if ev == nil || ev.Fields == nil {
		return ""
	}
	httpFields, ok := ev.Fields["http"].(map[string]any)
	if !ok {
		return ""
	}
	route, _ := httpFields["route"].(string)
	return route
}

func countEventDownstream(ev *Event) int {
	if ev == nil {
		return 0
	}
	total := 0
	for _, step := range ev.Steps {
		if step.Downstream != nil {
			total++
		}
	}
	return total
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func renderNextCursor(w io.Writer, cursor *string) {
	if cursor != nil && *cursor != "" {
		fmt.Fprintf(w, "\nnext_cursor: %s\n", *cursor)
	}
}

func formatBlastKey(key BlastKey) string {
	if key.Service == "" || key.Step == "" {
		return key.ErrorCode
	}
	return apiv2.FormatErrorFamily(ErrorFamily{Service: key.Service, Step: key.Step, ErrorCode: key.ErrorCode})
}

func truncateList(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, truncateID(id))
	}
	return strings.Join(out, ",")
}

func truncateID(id string) string {
	if len(id) <= 15 {
		return id
	}
	return id[:12] + "..."
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func renderPropagationBlock(w io.Writer, p *apiv2.PropagationSnapshot) {
	if p == nil || p.Latest == nil {
		fmt.Fprintln(w, "\nWhere did it start?")
		fmt.Fprintln(w, "  Propagation evidence unavailable")
		return
	}
	fmt.Fprintln(w, "\nWhere did it start?")
	if p.Latest.CaptureStatus != apiv2.CaptureStatusOK {
		fmt.Fprintf(w, "  Propagation evidence unavailable (%s) — retrying\n", p.Latest.CaptureStatus)
		return
	}
	fmt.Fprintf(w, "  Origin: %s / %s\n", p.Latest.OriginService, p.Latest.OriginStep)
	firstFailing, errCode := firstErrorStep(p.Latest)
	if firstFailing != "" {
		fmt.Fprintf(w, "  First failing step: %s  %s\n", firstFailing, errCode)
	}
	if len(p.Latest.Path) > 0 {
		names := make([]string, 0, len(p.Latest.Path))
		for _, s := range p.Latest.Path {
			names = append(names, s.Step)
		}
		fmt.Fprintf(w, "  %s\n", strings.Join(names, " → "))
	}
	fmt.Fprintf(w, "  sample trace: %s · captured %s ago\n",
		p.Latest.SampleTraceID, time.Since(p.Latest.CapturedAt).Round(time.Second))
}

func renderBlastBlock(w io.Writer, b *apiv2.BlastSnapshot) {
	if b == nil || b.Latest == nil {
		fmt.Fprintln(w, "\nHow bad is it?")
		fmt.Fprintln(w, "  Blast evidence unavailable")
		return
	}
	fmt.Fprintln(w, "\nHow bad is it?")
	if b.Opening != nil && blastDelta(b.Opening, b.Latest) {
		fmt.Fprintf(w, "  At open: %d req · %d svc · %s users\n",
			b.Opening.AffectedRequests, b.Opening.AffectedServices, usersStr(b.Opening.AffectedUsers))
		fmt.Fprintf(w, "  Now:     %d req · %d svc · %s users\n",
			b.Latest.AffectedRequests, b.Latest.AffectedServices, usersStr(b.Latest.AffectedUsers))
	} else {
		fmt.Fprintf(w, "  Now: %d req · %d svc · %s users\n",
			b.Latest.AffectedRequests, b.Latest.AffectedServices, usersStr(b.Latest.AffectedUsers))
	}
	if len(b.Latest.TopServices) > 0 {
		fmt.Fprintf(w, "  Top services: %s\n", strings.Join(b.Latest.TopServices, ", "))
	}
	fmt.Fprintf(w, "  captured %s ago\n", time.Since(b.Latest.CapturedAt).Round(time.Second))
}

func firstErrorStep(p *apiv2.PropagationEvidence) (step, code string) {
	if p == nil {
		return "", ""
	}
	for _, s := range p.Path {
		if s.Status == "error" {
			return s.Step, s.ErrorCode
		}
	}
	return "", ""
}

// blastDelta returns true if Opening and Latest differ on any user-visible
// impact field. CapturedAt and CaptureStatus are excluded — they change every
// tick and would force a permanent delta.
func blastDelta(o, l *apiv2.BlastEvidence) bool {
	if o == nil || l == nil {
		return false
	}
	if o.AffectedRequests != l.AffectedRequests {
		return true
	}
	if o.AffectedServices != l.AffectedServices {
		return true
	}
	if usersInt(o.AffectedUsers) != usersInt(l.AffectedUsers) {
		return true
	}
	if !slices.Equal(o.TopServices, l.TopServices) {
		return true
	}
	if len(o.SampledTraces) != len(l.SampledTraces) {
		return true
	}
	return false
}

func usersInt(u *int) int {
	if u == nil {
		return 0
	}
	return *u
}

func usersStr(u *int) string {
	if u == nil {
		return "?"
	}
	return fmt.Sprintf("%d", *u)
}

func RenderTriage(w io.Writer, rep *TriageReport) int {
	fmt.Fprintf(w, "Triage report  incident=%s  window=%s  confidence=%s\n",
		rep.IncidentRef.ID, rep.IncidentRef.Window, rep.Confidence)
	fmt.Fprintf(w, "  hash: %s\n\n", rep.ReportHash)

	fmt.Fprintln(w, "Blast")
	fmt.Fprintf(w, "  requests=%d  users=%d  services=%d\n",
		rep.BlastSnapshot.Requests, rep.BlastSnapshot.Users, rep.BlastSnapshot.Services)
	for _, f := range rep.BlastSnapshot.TopErrorFamilies {
		fmt.Fprintf(w, "  %s/%s/%s  count=%d\n", f.Service, f.Step, f.ErrorCode, f.Count)
	}
	fmt.Fprintln(w)

	if len(rep.SampleTraces) > 0 {
		fmt.Fprintln(w, "Sample traces")
		for _, s := range rep.SampleTraces {
			fmt.Fprintf(w, "  %s  %s\n", s.TraceID, s.Summary)
		}
		fmt.Fprintln(w)
	}

	if len(rep.Signals) > 0 {
		fmt.Fprintln(w, "Signals")
		for _, s := range rep.Signals {
			fmt.Fprintf(w, "  %s  type=%s  evidence=%v\n", s.ID, s.Type, s.EvidenceIDs)
		}
		fmt.Fprintln(w)
	}

	if len(rep.NextChecks) > 0 {
		fmt.Fprintln(w, "Next checks")
		for _, c := range rep.NextChecks {
			fmt.Fprintf(w, "  - %s\n", c.Prompt)
		}
	}
	return 0
}
