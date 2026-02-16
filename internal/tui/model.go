package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

type activeView int

const (
	viewDashboard activeView = iota
	viewStory
	viewHelp
)

type tickMsg time.Time

// Model is the root bubbletea model. It dispatches messages to sub-models
// and handles view switching.
type Model struct {
	activeView   activeView
	prevView     activeView
	width, height int
	dashboard    DashboardModel
	story        StoryModel
	api          *APIClient
	pollInterval time.Duration
}

func NewModel(client *APIClient, interval time.Duration) Model {
	return Model{
		activeView:   viewDashboard,
		dashboard:    NewDashboardModel(),
		story:        NewStoryModel(),
		api:          client,
		pollInterval: interval,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.api.FetchOverview("5m", 20), m.tick())
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tickMsg:
		return m, tea.Batch(m.api.FetchOverview("5m", 20), m.tick())
	case overviewMsg:
		m.dashboard.UpdateOverview(OverviewResponse(msg))
	case storyMsg:
		m.story.UpdateStory(StoryResponse(msg))
		m.activeView = viewStory
	case errMsg:
		m.dashboard.SetError(msg.err)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Quit from dashboard (when not filtering)
	if key.Matches(msg, Keys.Quit) && m.activeView == viewDashboard && m.dashboard.CanQuit() {
		return m, tea.Quit
	}

	switch m.activeView {
	case viewDashboard:
		cmd, nav := m.dashboard.HandleKey(msg, m.api.FetchStory, func() tea.Cmd {
			return m.api.FetchOverview("5m", 20)
		})
		if nav != nil {
			m.prevView = viewDashboard
			m.activeView = *nav
		}
		return m, cmd

	case viewStory:
		if nav := m.story.HandleKey(msg); nav != nil {
			m.prevView = viewStory
			m.activeView = *nav
		}

	case viewHelp:
		m.activeView = m.prevView // any key exits help
	}
	return m, nil
}

func (m Model) View() string {
	switch m.activeView {
	case viewStory:
		return m.story.View(m.width, m.height)
	case viewHelp:
		return HelpView(m.width, m.height)
	default:
		return m.dashboard.View(m.width, m.height)
	}
}
