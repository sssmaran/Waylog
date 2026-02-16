package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func HelpView(width, height int) string {
	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(colorCyan).Render("Keybindings"))
	lines = append(lines, "")
	lines = append(lines, "  j / ↓        Move down")
	lines = append(lines, "  k / ↑        Move up")
	lines = append(lines, "  enter        Inspect trace")
	lines = append(lines, "  esc          Back / clear filter")
	lines = append(lines, "  /            Filter traces")
	lines = append(lines, "  r            Refresh")
	lines = append(lines, "  ?            Toggle help")
	lines = append(lines, "  q            Quit")
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Render("Press any key to close"))

	content := strings.Join(lines, "\n")
	boxed := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorCyan).
		Padding(1, 2).
		Render(content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, boxed)
}
