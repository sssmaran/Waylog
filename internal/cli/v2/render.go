package cliv2

import (
	"encoding/json"
	"fmt"
	"io"
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
	fmt.Fprintf(w, "v2_reads: %s\n", enabledLabel(resp.V2Reads.Enabled))
	fmt.Fprintf(w, "otlp_http_traces: %s\n", enabledLabel(resp.OTLP.HTTPTraces))
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
