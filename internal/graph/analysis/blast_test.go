package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestBlastRadius_BasicImpact(t *testing.T) {
	st := graphstore.NewStore()
	b := build.NewBuilder()
	now := time.Now()

	ev := testutil.MakeEvent(
		testutil.WithService("payment"),
		testutil.WithError("PMT_502", "bad gateway"),
		testutil.WithStatusCode(502),
		testutil.WithTimestamp(now),
	)
	st.Merge(b.Build(ev))

	snap := st.Snapshot()
	result := ComputeBlastRadius(snap, "PMT_502", now.Add(-1*time.Hour), now.Add(time.Second))

	if result.AffectedRequests < 1 {
		t.Errorf("expected at least 1 affected request, got %d", result.AffectedRequests)
	}
	if result.AffectedUsers < 1 {
		t.Errorf("expected at least 1 affected user, got %d", result.AffectedUsers)
	}
	if result.Services == nil {
		t.Fatal("Services slice must never be nil")
	}
	if len(result.Services) < 1 {
		t.Errorf("expected at least 1 service, got %d", len(result.Services))
	}

	// Verify service name is "payment"
	found := false
	for _, s := range result.Services {
		if strings.Contains(s.Service, "payment") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected payment service in results, got %v", result.Services)
	}
}

func TestBlastRadius_ZeroResults(t *testing.T) {
	t.Run("empty graph", func(t *testing.T) {
		st := graphstore.NewStore()
		snap := st.Snapshot()
		now := time.Now()
		result := ComputeBlastRadius(snap, "NONEXISTENT", now.Add(-1*time.Hour), now)

		if result.AffectedRequests != 0 {
			t.Errorf("expected 0 affected requests, got %d", result.AffectedRequests)
		}
		if result.AffectedUsers != 0 {
			t.Errorf("expected 0 affected users, got %d", result.AffectedUsers)
		}
		if result.Services == nil {
			t.Fatal("Services slice must never be nil, even when empty")
		}
		if len(result.Services) != 0 {
			t.Errorf("expected empty services, got %v", result.Services)
		}
	})

	t.Run("nonexistent error code", func(t *testing.T) {
		st := graphstore.NewStore()
		b := build.NewBuilder()
		now := time.Now()

		ev := testutil.MakeEvent(
			testutil.WithService("payment"),
			testutil.WithError("PMT_502", "bad gateway"),
			testutil.WithStatusCode(502),
			testutil.WithTimestamp(now),
		)
		st.Merge(b.Build(ev))

		snap := st.Snapshot()
		result := ComputeBlastRadius(snap, "NONEXISTENT", now.Add(-1*time.Hour), now.Add(time.Second))

		if result.AffectedRequests != 0 {
			t.Errorf("expected 0 affected requests, got %d", result.AffectedRequests)
		}
		if result.AffectedUsers != 0 {
			t.Errorf("expected 0 affected users, got %d", result.AffectedUsers)
		}
		if result.Services == nil {
			t.Fatal("Services slice must never be nil")
		}
	})
}
