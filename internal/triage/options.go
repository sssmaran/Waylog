// Package triage builds the TriageReport for an incident.
// The Report type is the public artifact (pkg/triage); this package is the orchestrator.
package triage

import (
	"fmt"
	"time"
)

const defaultWindow = 15 * time.Minute

type BuildOptions struct {
	Window   time.Duration
	Snapshot bool
	Now      time.Time
}

func ParseBuildOptions(window string, snapshot bool, now time.Time) (BuildOptions, error) {
	w := defaultWindow
	if window != "" {
		parsed, err := time.ParseDuration(window)
		if err != nil {
			return BuildOptions{}, fmt.Errorf("triage: invalid window %q: %w", window, err)
		}
		w = parsed
	}
	return BuildOptions{Window: w, Snapshot: snapshot, Now: now}, nil
}
