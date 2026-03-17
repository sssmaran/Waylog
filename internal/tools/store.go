package tools

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
)

type Store interface {
	Snapshot() *core.Graph
	SummarizeWindow(start, end time.Time) graphstore.WindowSummary
	ForEachRequestFact(start, end time.Time, fn func(graphstore.RequestFacts))
	ErrorIndex(errorCode string) ([]string, bool)
}
