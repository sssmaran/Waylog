package graph

import "time"

// RequestFacts is the minimal per-request “semantic envelope” needed for fast summaries.
// It is derived from the graph at merge time, not from raw events (semantics stay locked).
type RequestFacts struct {
	RequestID string
	SeenAt    time.Time // use Request.LastSeen as the request timestamp

	// Typically one service, but keep slice for future multi-service chains
	Services []string // node IDs of service nodes (or service names if you prefer)
	Errors   []string // node IDs of error nodes (or error codes if stored as labels/attrs)
	Flags    []string // node IDs of flag nodes

	// Common dimensions (optional; only fill if present in request attrs)
	Tier    string
	Version string

	LatencyMs int64
	Status    string
}

// Counters are all-time rollups. Optional but nice for non-windowed commands.
type Counters struct {
	// errorID -> count
	ErrorCount map[string]int

	// serviceID -> errorID -> count
	ServiceErrorCount map[string]map[string]int

	// flagID -> errorID -> count
	FlagErrorCount map[string]map[string]int
}

func NewCounters() *Counters {
	return &Counters{
		ErrorCount:        map[string]int{},
		ServiceErrorCount: map[string]map[string]int{},
		FlagErrorCount:    map[string]map[string]int{},
	}
}
