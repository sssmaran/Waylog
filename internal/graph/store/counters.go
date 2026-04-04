package store

import "time"

// RequestFacts is the minimal per-request "semantic envelope" needed for fast summaries.
// It is derived from the graph at merge time, not from raw events (semantics stay locked).
type RequestFacts struct {
	RequestID string
	TraceID   string
	SeenAt    time.Time // use Request.LastSeen as the request timestamp

	// Canonical owner (root span's service). Empty until root merges.
	RootService string

	// Typically one service, but keep slice for future multi-service chains
	Services     []string // service names from handled_by edges
	Errors       []string // error codes from failed_with edges
	FeatureFlags []string // flag names from request attrs or used_flag edges

	UserID     string
	UserTier   string
	UserVIP    bool
	UserRegion string

	Version   string
	LatencyMs int64
	Status    string
}

// HasFeatureFlag reports whether the request fact contains the named flag.
func (f RequestFacts) HasFeatureFlag(flag string) bool {
	for _, current := range f.FeatureFlags {
		if current == flag {
			return true
		}
	}
	return false
}

// HasError reports whether the request fact contains the named error code.
func (f RequestFacts) HasError(code string) bool {
	for _, current := range f.Errors {
		if current == code {
			return true
		}
	}
	return false
}

type ServiceStats struct {
	Invocations int
	Errors      int
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
