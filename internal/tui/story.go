package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StoryModel holds story view state.
type StoryModel struct {
	story   Story
	context TraceContext
	loaded  bool
}

func NewStoryModel() StoryModel {
	return StoryModel{}
}

func (s *StoryModel) UpdateStory(resp StoryResponse) {
	s.story = resp.Story
	s.context = resp.Context
	s.loaded = true
}

// HandleKey processes key input for the story view.
// Returns a view to switch to, or nil to stay.
func (s *StoryModel) HandleKey(msg tea.KeyMsg) *activeView {
	switch {
	case key.Matches(msg, Keys.Back), key.Matches(msg, Keys.Quit):
		v := viewDashboard
		return &v
	case key.Matches(msg, Keys.Help):
		v := viewHelp
		return &v
	}
	return nil
}

func (s StoryModel) View(width, height int) string {
	if !s.loaded {
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center,
			helpBarStyle.Render("Loading trace story..."))
	}

	var b strings.Builder

	// Title
	traceID := s.story.TraceID
	if len(traceID) > 16 {
		traceID = traceID[:12] + "..."
	}
	b.WriteString(titleStyle.Render("Trace "+traceID) + "\n" + separator(width) + "\n")

	// Two-column layout via shared renderColumns
	leftWidth := width/2 - 2
	b.WriteString(renderColumns(s.renderHopChain(), s.renderContext(), leftWidth))

	// Summary
	b.WriteString(separator(width) + "\n")
	status := successStyle.Render("SUCCESS")
	if !s.story.Success {
		label := "FAILED"
		if s.story.FirstFailHop != nil {
			label += fmt.Sprintf(" (first fail: %s)", s.story.FirstFailHop.Service)
		}
		status = failStyle.Render(label)
	}
	b.WriteString(fmt.Sprintf("Overall: %s  Hops: %d\n", status, s.story.HopCount))
	b.WriteString(helpBarStyle.Render("esc: back  q: dashboard  ?: help"))

	return b.String()
}

func (s StoryModel) renderHopChain() string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("Hop Chain") + "\n\n")

	for i, hop := range s.story.Chain {
		icon := successStyle.Render("✓")
		statusStr := StatusColor(hop.StatusCode).Render(fmt.Sprintf("%d", hop.StatusCode))
		if !hop.Success {
			icon = failStyle.Render("✗")
			statusStr = failStyle.Render(fmt.Sprintf("%d", hop.StatusCode))
		}

		service := hop.Service
		if s.story.FirstFailHop != nil && hop.SpanID == s.story.FirstFailHop.SpanID {
			service = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(service)
		}

		b.WriteString(fmt.Sprintf("[%d] %s %s\n     %s  %dms\n", i+1, icon, service, statusStr, hop.LatencyMs))
		if hop.ErrorCode != "" {
			b.WriteString(failStyle.Render("     └ "+hop.ErrorCode) + "\n")
		}
		if i < len(s.story.Chain)-1 {
			b.WriteString("     │\n")
		}
	}

	return b.String()
}

func (s StoryModel) renderContext() string {
	var b strings.Builder
	b.WriteString(labelStyle.Render("Context") + "\n\n")

	ctx := s.context
	if ctx.UserID != "" {
		line := "User: " + ctx.UserID
		if ctx.UserTier != "" {
			line += fmt.Sprintf(" (%s)", ctx.UserTier)
		}
		b.WriteString(line + "\n")
	}
	if ctx.UserRegion != "" {
		b.WriteString("Region: " + ctx.UserRegion + "\n")
	}
	if ctx.Flow != "" {
		b.WriteString("Flow: " + ctx.Flow + "\n")
	}
	if len(ctx.Flags) > 0 {
		b.WriteString("Flags: " + strings.Join(ctx.Flags, ", ") + "\n")
	}

	b.WriteString("\n" + labelStyle.Render("Commands") + "\n")
	cmdStyle := helpBarStyle
	b.WriteString(cmdStyle.Render(fmt.Sprintf("waylog \"trace summary %s\"", s.story.TraceID)) + "\n")
	b.WriteString(cmdStyle.Render(fmt.Sprintf("waylog \"explain request %s\"", s.story.TraceID)) + "\n")

	return b.String()
}
