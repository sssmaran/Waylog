package store

import (
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func TestSummarizeWindow_CountsAndQuantiles(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	now := time.Now()

	// 5 requests with varying latencies: 10, 20, 30, 40, 50
	for i, lat := range []int64{10, 20, 30, 40, 50} {
		ev := testutil.MakeEvent(
			testutil.WithTraceID(padHex(i)),
			testutil.WithLatency(lat),
			testutil.WithTimestamp(now.Add(-time.Duration(i)*time.Second)),
		)
		s.Merge(b.Build(ev))
	}

	// 2 failure requests
	for i, code := range []string{"ERR_A", "ERR_B"} {
		ev := testutil.MakeEvent(
			testutil.WithTraceID(padHex(100+i)),
			testutil.WithLatency(100),
			testutil.WithError(code, "msg"),
			testutil.WithStatusCode(500),
			testutil.WithTimestamp(now.Add(-time.Duration(i)*time.Second)),
		)
		s.Merge(b.Build(ev))
	}

	summary := s.SummarizeWindow(now.Add(-time.Minute), now.Add(time.Minute))

	if summary.TotalRequests != 7 {
		t.Errorf("TotalRequests = %d, want 7", summary.TotalRequests)
	}
	if summary.TotalFailures != 2 {
		t.Errorf("TotalFailures = %d, want 2", summary.TotalFailures)
	}

	// 7 latencies sorted: 10, 20, 30, 40, 50, 100, 100
	// P50: index = 50*7/100 = 3 → 40
	// P95: index = 95*7/100 = 6 → 100
	// P99: index = 99*7/100 = 6 → 100
	if summary.LatencyP50 != 40 {
		t.Errorf("LatencyP50 = %d, want 40", summary.LatencyP50)
	}
	if summary.LatencyP95 != 100 {
		t.Errorf("LatencyP95 = %d, want 100", summary.LatencyP95)
	}
	if summary.LatencyP99 != 100 {
		t.Errorf("LatencyP99 = %d, want 100", summary.LatencyP99)
	}
}

func TestSummarizeWindow_DedupServicesFlags(t *testing.T) {
	s := NewStore()
	b := build.NewBuilder()
	now := time.Now()

	// Create two events for the same trace (same service → duplicate edges)
	traceID := padHex(1)
	for i := 0; i < 2; i++ {
		ev := testutil.MakeEvent(
			testutil.WithTraceID(traceID),
			testutil.WithService("svc-a"),
			testutil.WithFeatureFlags("flag-x"),
			testutil.WithTimestamp(now),
			testutil.WithSpanID(padSpan(i)),
		)
		s.Merge(b.Build(ev))
	}

	// Create a second request with different trace
	ev2 := testutil.MakeEvent(
		testutil.WithTraceID(padHex(2)),
		testutil.WithService("svc-a"),
		testutil.WithFeatureFlags("flag-x"),
		testutil.WithTimestamp(now),
	)
	s.Merge(b.Build(ev2))

	summary := s.SummarizeWindow(now.Add(-time.Minute), now.Add(time.Minute))

	// svc-a should count per-request, not per-event-merge
	// Each request fact has deduplicated services
	if summary.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", summary.TotalRequests)
	}

	// ServiceRequestCount: each request contributes 1 (deduped)
	for svcID, count := range summary.ServiceRequestCount {
		if count > 2 {
			t.Errorf("ServiceRequestCount[%s] = %d, want <= 2 (deduped per request)", svcID, count)
		}
	}
}

func TestSummarizeWindow_UsesFlattenedFeatureFlags(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.requestFacts["req-1"] = RequestFacts{
		RequestID:    "req-1",
		SeenAt:       now,
		Services:     []string{"checkout"},
		Errors:       []string{"ERR_A"},
		FeatureFlags: []string{"flag-a", "flag-b", "flag-a"},
		LatencyMs:    25,
		Status:       "error",
	}
	s.requestFacts["req-2"] = RequestFacts{
		RequestID:    "req-2",
		SeenAt:       now.Add(time.Second),
		Services:     []string{"checkout"},
		FeatureFlags: []string{"flag-b"},
		LatencyMs:    75,
		Status:       "ok",
	}

	summary := s.SummarizeWindow(now.Add(-time.Minute), now.Add(time.Minute))

	if summary.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalFailures != 1 {
		t.Fatalf("TotalFailures = %d, want 1", summary.TotalFailures)
	}
	if got := summary.ServiceRequestCount["checkout"]; got != 2 {
		t.Fatalf("ServiceRequestCount[checkout] = %d, want 2", got)
	}
	if got := summary.FlagRequestCount["flag-a"]; got != 1 {
		t.Fatalf("FlagRequestCount[flag-a] = %d, want 1", got)
	}
	if got := summary.FlagRequestCount["flag-b"]; got != 2 {
		t.Fatalf("FlagRequestCount[flag-b] = %d, want 2", got)
	}
	if got := summary.FlagErrorCount["flag-a"]["ERR_A"]; got != 1 {
		t.Fatalf("FlagErrorCount[flag-a][ERR_A] = %d, want 1", got)
	}
	if got := summary.FlagErrorCount["flag-b"]["ERR_A"]; got != 1 {
		t.Fatalf("FlagErrorCount[flag-b][ERR_A] = %d, want 1", got)
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		sorted []int64
		pct    int
		want   int64
	}{
		{"empty", nil, 50, 0},
		{"single", []int64{42}, 50, 42},
		{"single p99", []int64{42}, 99, 42},
		{"ten elements p50", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 50, 5},
		{"ten elements p95", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 95, 10},
		{"ten elements p99", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 99, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.sorted, tt.pct)
			if got != tt.want {
				t.Errorf("percentile(%v, %d) = %d, want %d", tt.sorted, tt.pct, got, tt.want)
			}
		})
	}
}

func padHex(n int) string {
	s := ""
	for len(s) < 31 {
		s += "0"
	}
	hex := "0123456789abcdef"
	if n < 16 {
		return s + string(hex[n])
	}
	return s[:30] + string(hex[n/16]) + string(hex[n%16])
}

func padSpan(n int) string {
	s := ""
	for len(s) < 15 {
		s += "0"
	}
	hex := "0123456789abcdef"
	return s + string(hex[n])
}
