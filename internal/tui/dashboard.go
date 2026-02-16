package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DashboardModel holds the dashboard view state.
type DashboardModel struct {
	overview    OverviewResponse
	selectedIdx int
	filterText  string
	filtering   bool
	err         error
}

func NewDashboardModel() DashboardModel {
	return DashboardModel{}
}

// UpdateOverview updates all stats and traces from an overview response.
func (d *DashboardModel) UpdateOverview(resp OverviewResponse) {
	d.overview = resp
	d.err = nil
	if d.selectedIdx >= len(d.overview.RecentTraces) {
		d.selectedIdx = 0
	}
}

// SetError sets the error state displayed in the footer.
func (d *DashboardModel) SetError(err error) { d.err = err }

// CanQuit returns true if the dashboard is in a state where quit is allowed.
func (d *DashboardModel) CanQuit() bool { return !d.filtering }

// HandleKey processes key input for the dashboard.
// onInspect is called when the user selects a trace (returns a fetch command).
// onRefresh is called when the user requests a data refresh.
func (d *DashboardModel) HandleKey(msg tea.KeyMsg, onInspect func(string) tea.Cmd, onRefresh func() tea.Cmd) (tea.Cmd, *activeView) {
	if d.filtering {
		return d.handleFilterKey(msg), nil
	}

	switch {
	case key.Matches(msg, Keys.Down):
		d.moveDown()
	case key.Matches(msg, Keys.Up):
		d.moveUp()
	case key.Matches(msg, Keys.Enter):
		if id := d.selectedTraceID(); id != "" {
			return onInspect(id), nil
		}
	case key.Matches(msg, Keys.Filter):
		d.filtering = true
		d.filterText = ""
	case key.Matches(msg, Keys.Refresh):
		return onRefresh(), nil
	case key.Matches(msg, Keys.Help):
		v := viewHelp
		return nil, &v
	}
	return nil, nil
}

func (d *DashboardModel) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, Keys.Back):
		d.filtering = false
		d.filterText = ""
	case key.Matches(msg, Keys.Enter):
		d.filtering = false
	default:
		if msg.Type == tea.KeyBackspace && len(d.filterText) > 0 {
			d.filterText = d.filterText[:len(d.filterText)-1]
		} else if msg.Type == tea.KeyRunes {
			d.filterText += string(msg.Runes)
		}
	}
	return nil
}

func (d *DashboardModel) moveDown() {
	if n := len(d.filteredTraces()); n > 0 && d.selectedIdx < n-1 {
		d.selectedIdx++
	}
}

func (d *DashboardModel) moveUp() {
	if d.selectedIdx > 0 {
		d.selectedIdx--
	}
}

func (d *DashboardModel) selectedTraceID() string {
	filtered := d.filteredTraces()
	if d.selectedIdx < len(filtered) {
		return filtered[d.selectedIdx].TraceID
	}
	return ""
}

func (d *DashboardModel) filteredTraces() []TraceEntry {
	if d.filterText == "" {
		return d.overview.RecentTraces
	}
	needle := strings.ToLower(d.filterText)
	var result []TraceEntry
	for _, t := range d.overview.RecentTraces {
		if strings.Contains(strings.ToLower(t.TraceID), needle) ||
			strings.Contains(strings.ToLower(t.EventName), needle) ||
			strings.Contains(strconv.Itoa(t.StatusCode), needle) {
			result = append(result, t)
		}
	}
	return result
}

// View renders the dashboard.
func (d *DashboardModel) View(width, height int) string {
	var b strings.Builder

	// Header
	b.WriteString(renderBanner(width))
	b.WriteString(liveIndicator + "  " +
		statusBarStyle.Render(fmt.Sprintf("Requests: %d  Failures: %d  Error Rate: %.1f%%",
			d.overview.TotalRequests, d.overview.TotalFailures, d.overview.ErrorRate)))
	b.WriteString("\n" + separator(width) + "\n")

	// Two-column layout
	leftWidth := width/2 - 2
	rightWidth := width - leftWidth - 3
	b.WriteString(renderColumns(
		d.renderTraces(leftWidth, height-9),
		d.renderTopErrors(rightWidth, height-9),
		leftWidth,
	))
	b.WriteString(separator(width) + "\n")

	// Footer
	if d.err != nil {
		b.WriteString(failStyle.Render(fmt.Sprintf("Error: %v", d.err)))
	} else if d.filtering {
		b.WriteString(labelStyle.Render("filter: ") + d.filterText + "█")
	} else {
		b.WriteString(helpBarStyle.Render("j/k: navigate  enter: inspect  /: filter  r: refresh  q: quit  ?: help"))
	}

	return b.String()
}

func (d *DashboardModel) renderTraces(width, maxRows int) string {
	var b strings.Builder
	const (
		traceColWidth   = 8
		statusColWidth  = 6
		codeColWidth    = 4
		latencyColWidth = 7
		nameColWidth    = 20
	)

	col := func(w int, s string) string {
		return lipgloss.NewStyle().Width(w).Render(s)
	}

	b.WriteString(labelStyle.Render("Recent Traces") + "\n")
	header := "  " +
		col(traceColWidth, "ID") + "  " +
		col(statusColWidth, "STATUS") + "  " +
		col(codeColWidth, "CODE") + "  " +
		col(latencyColWidth, "LATENCY") + "  " +
		col(nameColWidth, "NAME")
	b.WriteString(statusBarStyle.Render(header) + "\n")

	filtered := d.filteredTraces()
	if len(filtered) == 0 {
		b.WriteString(statusBarStyle.Render("  No traces"))
		return b.String()
	}

	for i, t := range filtered {
		if i >= maxRows-1 {
			break
		}

		traceShort := t.TraceID
		if len(traceShort) > 8 {
			traceShort = traceShort[:8]
		}

		status := successStyle.Render("OK")
		if !t.Success {
			status = failStyle.Render("FAIL")
		}

		name := t.EventName
		if len(name) > nameColWidth {
			name = name[:nameColWidth]
		}

		line := "  " +
			col(traceColWidth, traceShort) + "  " +
			col(statusColWidth, status) + "  " +
			col(codeColWidth, StatusColor(t.StatusCode).Render(fmt.Sprintf("%d", t.StatusCode))) + "  " +
			col(latencyColWidth, fmt.Sprintf("%dms", t.LatencyMs)) + "  " +
			col(nameColWidth, name)

		if i == d.selectedIdx {
			line = selectedRowStyle.Render("▸" + line[1:])
		}

		b.WriteString(line + "\n")
	}

	return b.String()
}

func (d *DashboardModel) renderTopErrors(width, maxRows int) string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("Top Errors") + "\n")

	if len(d.overview.TopErrors) == 0 {
		b.WriteString(statusBarStyle.Render("No errors"))
		return b.String()
	}

	maxCount := 1
	for _, e := range d.overview.TopErrors {
		if e.Count > maxCount {
			maxCount = e.Count
		}
	}

	for i, e := range d.overview.TopErrors {
		if i >= maxRows {
			break
		}
		barWidth := (e.Count * 10) / maxCount
		if barWidth < 1 && e.Count > 0 {
			barWidth = 1
		}
		b.WriteString(fmt.Sprintf("%-12s  %s  %d\n",
			e.Code, errorBarStyle.Render(strings.Repeat("█", barWidth)), e.Count))
	}

	return b.String()
}
