package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Colors used across views.
var (
	colorGreen  = lipgloss.Color("#00CC00")
	colorYellow = lipgloss.Color("#CCCC00")
	colorRed    = lipgloss.Color("#CC0000")
	colorCyan   = lipgloss.Color("#00CCCC")
	colorDim    = lipgloss.Color("#666666")
	colorHighBg = lipgloss.Color("#333366")
	colorWhite  = lipgloss.Color("#FFFFFF")
)

// ASCII art banner for the dashboard header.
var bannerLines = []string{
	`██╗    ██╗ █████╗ ██╗   ██╗██╗      ██████╗  ██████╗`,
	`██║    ██║██╔══██╗╚██╗ ██╔╝██║     ██╔═══██╗██╔════╝`,
	`██║ █╗ ██║███████║ ╚████╔╝ ██║     ██║   ██║██║  ███╗`,
	`██║███╗██║██╔══██║  ╚██╔╝  ██║     ██║   ██║██║   ██║`,
	`╚███╔███╔╝██║  ██║   ██║   ███████╗╚██████╔╝╚██████╔╝`,
	` ╚══╝╚══╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝ ╚═════╝  ╚═════╝`,
}

var gradientBannerLines = buildGradientBanner(bannerLines)

// Shared styles used by dashboard, story, and help views.
var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(colorCyan).Padding(0, 1)
	statusBarStyle   = lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)
	helpBarStyle     = lipgloss.NewStyle().Foreground(colorDim)
	labelStyle       = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	successStyle     = lipgloss.NewStyle().Foreground(colorGreen)
	failStyle        = lipgloss.NewStyle().Foreground(colorRed)
	errorBarStyle    = lipgloss.NewStyle().Foreground(colorRed)
	selectedRowStyle = lipgloss.NewStyle().Background(colorHighBg).Foreground(colorWhite).Bold(true)
	liveIndicator    = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("● Live")
)

// StatusColor returns the appropriate style for a status code.
func StatusColor(code int) lipgloss.Style {
	switch {
	case code >= 500:
		return failStyle
	case code >= 400:
		return lipgloss.NewStyle().Foreground(colorYellow)
	default:
		return successStyle
	}
}

// renderColumns renders two text blocks side-by-side with a │ divider.
func renderColumns(left, right string, leftWidth int) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	var b strings.Builder
	padder := lipgloss.NewStyle().Width(leftWidth)
	for i := 0; i < maxLines; i++ {
		var l, r string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		b.WriteString(padder.Render(l))
		b.WriteString(" │ ")
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

// separator returns a horizontal line.
func separator(width int) string {
	return strings.Repeat("─", width)
}

// renderBanner renders a centered dashboard title with a narrow-screen fallback.
func renderBanner(width int) string {
	artWidth := 0
	for _, line := range gradientBannerLines {
		if w := lipgloss.Width(line); w > artWidth {
			artWidth = w
		}
	}

	if width <= 0 || width < artWidth+2 {
		return titleStyle.Render("WAYLOG") + "\n"
	}

	var b strings.Builder
	for _, line := range gradientBannerLines {
		pad := (width - lipgloss.Width(line)) / 2
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func buildGradientBanner(lines []string) []string {
	h := len(lines)
	if h == 0 {
		return nil
	}
	maxW := 1
	for _, line := range lines {
		if w := lipgloss.Width(line); w > maxW {
			maxW = w
		}
	}

	// Strong 3-stop cyan gradient for a filled neon look.
	top := rgb{r: 140, g: 255, b: 255}
	mid := rgb{r: 58, g: 224, b: 246}
	bottom := rgb{r: 0, g: 170, b: 235}

	out := make([]string, 0, h)
	for y, line := range lines {
		runes := []rune(line)
		var b strings.Builder
		for x, ch := range runes {
			if ch == ' ' {
				b.WriteRune(ch)
				continue
			}
			vy := 0.0
			if h > 1 {
				vy = float64(y) / float64(h-1)
			}
			vx := 0.0
			if maxW > 1 {
				vx = float64(x) / float64(maxW-1)
			}
			t := 0.80*vy + 0.20*vx
			c := threeStopColor(top, mid, bottom, t)
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Render(string(ch)))
		}
		out = append(out, b.String())
	}
	return out
}

type rgb struct {
	r int
	g int
	b int
}

func (c rgb) Hex() string {
	return fmt.Sprintf("#%02X%02X%02X", clamp(c.r), clamp(c.g), clamp(c.b))
}

func mixColor(a, b rgb, t float64) rgb {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return rgb{
		r: int(float64(a.r) + (float64(b.r)-float64(a.r))*t),
		g: int(float64(a.g) + (float64(b.g)-float64(a.g))*t),
		b: int(float64(a.b) + (float64(b.b)-float64(a.b))*t),
	}
}

func threeStopColor(a, m, b rgb, t float64) rgb {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	if t <= 0.5 {
		return mixColor(a, m, t*2)
	}
	return mixColor(m, b, (t-0.5)*2)
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
