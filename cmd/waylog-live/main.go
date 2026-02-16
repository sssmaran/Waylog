package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sssmaran/WaylogCLI/internal/tui"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "ingest server URL")
	interval := flag.Duration("interval", 2*time.Second, "poll interval")
	flag.Parse()
	if *interval <= 0 {
		*interval = 2 * time.Second
	}

	client := tui.NewAPIClient(*addr)
	model := tui.NewModel(client, *interval)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
