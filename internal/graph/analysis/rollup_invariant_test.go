package analysis

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

func TestRollupInvariantRootCauseStaysBelowNaivePropagation(t *testing.T) {
	s := store.NewStore()
	ts := tracestore.NewStore()
	b := build.NewBuilder()
	now := time.Now().UTC()

	const failedRequests = 3
	for i := range failedRequests {
		ingestCascade(t, s, ts, b, i, now.Add(-20*time.Second))
	}

	summary := RollupWindow(graphOf(s), s, ts, now.Add(-time.Minute), now.Add(time.Minute))
	rootCauseCount := summary.PrimaryErrorCount["PMT_502"]
	naivePropagatedCount := failedRequests * 3

	if rootCauseCount != failedRequests {
		t.Fatalf("PMT_502 root-cause count = %d, want %d", rootCauseCount, failedRequests)
	}
	if rootCauseCount >= naivePropagatedCount {
		t.Fatalf("root-cause count should stay below naive propagated count: root=%d naive=%d", rootCauseCount, naivePropagatedCount)
	}
}
