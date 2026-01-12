package ingest

import "github.com/sssmaran/WaylogCLI/internal/graph/store"

// GlobalGraphStore holds all retained events as a graph.
// This is process-scoped for now (no persistence yet).
var GlobalGraphStore = store.NewStore()
