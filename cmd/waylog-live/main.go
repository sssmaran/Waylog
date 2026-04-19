package main

import (
	"context"
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
	dev := flag.Bool("dev", false, "use SSE stream for live updates instead of polling")
	flag.Parse()
	if *interval <= 0 {
		*interval = 2 * time.Second
	}

	client := tui.NewAPIClient(*addr)
	model := tui.NewModel(client, *interval)

	if *dev {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch, err := client.StartDashboardStream(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dev stream unavailable (%v) — falling back to polling\n", err)
		} else {
			model = model.WithStream(ch)
		}
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
