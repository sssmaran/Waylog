package analysis

import (
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestDetectFailurePatternsIgnoresSpanOriginFailedWithEdges(t *testing.T) {
	s := graphstore.NewStore()
	b := build.NewBuilder()

	ev := testutil.MakeEvent(
		testutil.WithTraceID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithService("checkout"),
		testutil.WithFlow("checkout"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "payment failed"),
		testutil.WithUser("u-1", "standard", "us-east-1"),
	)
	s.Merge(b.Build(ev))

	patterns := DetectFailurePatterns(s.Snapshot())
	if len(patterns) != 1 {
		t.Fatalf("patterns len = %d, want 1", len(patterns))
	}
	if patterns[0].Count != 1 {
		t.Fatalf("pattern count = %d, want 1", patterns[0].Count)
	}
	if patterns[0].ErrorCode != "PMT_502" {
		t.Fatalf("error_code = %q, want PMT_502", patterns[0].ErrorCode)
	}
	if patterns[0].UserTier != "standard" {
		t.Fatalf("user_tier = %q, want standard", patterns[0].UserTier)
	}
	if patterns[0].Flow != "checkout" {
		t.Fatalf("flow = %q, want checkout", patterns[0].Flow)
	}
}
