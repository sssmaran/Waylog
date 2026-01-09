package ingest

import "github.com/sssmaran/WaylogCLI/internal/graph"

// GlobalGraphStore holds all retained events as a graph.
// This is process-scoped for now (no persistence yet).
var GlobalGraphStore = graph.NewStore()
