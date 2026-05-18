package triage

import (
	"testing"
	"time"
)

func TestParseBuildOptions(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		window   string
		snapshot bool
		want     time.Duration
	}{
		{"default window", "", false, 15 * time.Minute},
		{"explicit 30m", "30m", false, 30 * time.Minute},
		{"snapshot flag honored", "15m", true, 15 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := ParseBuildOptions(tc.window, tc.snapshot, now)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if opts.Window != tc.want {
				t.Fatalf("window = %s, want %s", opts.Window, tc.want)
			}
			if opts.Snapshot != tc.snapshot {
				t.Fatalf("snapshot = %v, want %v", opts.Snapshot, tc.snapshot)
			}
			if !opts.Now.Equal(now) {
				t.Fatalf("Now should be passed through")
			}
		})
	}
}

func TestParseBuildOptionsBadWindow(t *testing.T) {
	if _, err := ParseBuildOptions("forever", false, time.Now()); err == nil {
		t.Fatalf("expected error for bad window")
	}
}
