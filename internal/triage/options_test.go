package triage

import (
	"testing"
	"time"
)

func TestBuildOptionsDefaults(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	opts, err := ParseBuildOptions("", false, now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Window != 15*time.Minute {
		t.Fatalf("default window should be 15m, got %s", opts.Window)
	}
	if opts.Snapshot {
		t.Fatalf("default snapshot should be false")
	}
	if !opts.Now.Equal(now) {
		t.Fatalf("Now should be passed through")
	}
}

func TestBuildOptionsWindowParse(t *testing.T) {
	now := time.Now()
	opts, err := ParseBuildOptions("30m", false, now)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Window != 30*time.Minute {
		t.Fatalf("got %s want 30m", opts.Window)
	}
}

func TestBuildOptionsBadWindow(t *testing.T) {
	if _, err := ParseBuildOptions("forever", false, time.Now()); err == nil {
		t.Fatalf("expected error for bad window")
	}
}

func TestBuildOptionsSnapshotFlag(t *testing.T) {
	opts, err := ParseBuildOptions("15m", true, time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.Snapshot {
		t.Fatalf("snapshot flag not honored")
	}
}
