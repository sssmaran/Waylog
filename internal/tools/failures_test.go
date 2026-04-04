package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/core"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tracestore"
)

type factStore struct {
	facts    []store.RequestFacts
	errors   map[string][]string
	snapshot *core.Graph
}

func (s factStore) Snapshot() *core.Graph {
	if s.snapshot != nil {
		return s.snapshot
	}
	return core.New()
}

func (s factStore) SummarizeWindow(start, end time.Time) store.WindowSummary {
	return store.WindowSummary{}
}

func (s factStore) ForEachRequestFact(start, end time.Time, fn func(store.RequestFacts)) {
	for _, f := range s.facts {
		if !f.SeenAt.IsZero() {
			if f.SeenAt.Before(start) || f.SeenAt.After(end) {
				continue
			}
		}
		fn(f)
	}
}

func (s factStore) ErrorIndex(errorCode string) ([]string, bool) {
	ids, ok := s.errors[errorCode]
	if !ok {
		return nil, false
	}
	return append([]string(nil), ids...), true
}

func (s factStore) TraceStore() *tracestore.Store { return nil }

func TestBlastRadius_SeverityScore(t *testing.T) {
	s := store.NewStore()
	b := build.NewBuilder()

	// 3 requests failing with same error, one VIP user, one premium user
	ev1 := testutil.MakeEvent(
		testutil.WithTraceID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		testutil.WithUser("user-vip", "premium", "us"),
		testutil.WithVIP(true),
		testutil.WithError("BLAST_ERR", "boom"),
		testutil.WithStatusCode(500),
		testutil.WithService("svc-a"),
	)
	ev2 := testutil.MakeEvent(
		testutil.WithTraceID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		testutil.WithUser("user-premium", "premium", "us"),
		testutil.WithError("BLAST_ERR", "boom"),
		testutil.WithStatusCode(500),
		testutil.WithService("svc-a"),
	)
	ev3 := testutil.MakeEvent(
		testutil.WithTraceID("cccccccccccccccccccccccccccccccc"),
		testutil.WithUser("user-standard", "standard", "us"),
		testutil.WithError("BLAST_ERR", "boom"),
		testutil.WithStatusCode(500),
		testutil.WithService("svc-b"),
	)

	s.Merge(b.Build(ev1))
	s.Merge(b.Build(ev2))
	s.Merge(b.Build(ev3))

	params, _ := json.Marshal(blastInput{ErrorCode: "BLAST_ERR", IncludeServices: true})
	result, err := handleBlastRadius(context.Background(), s, params)
	if err != nil {
		t.Fatal(err)
	}

	out := result.(blastOutput)

	if out.AffectedRequests != 3 {
		t.Errorf("AffectedRequests = %d, want 3", out.AffectedRequests)
	}
	if out.VIPUsers != 1 {
		t.Errorf("VIPUsers = %d, want 1", out.VIPUsers)
	}

	// Default weights: request=1, vip=10, premium=3, service=5
	// Score = 3*1 + 1*10 + 2*3 + 2*5 = 3 + 10 + 6 + 10 = 29
	if out.SeverityScore != 29 {
		t.Errorf("SeverityScore = %f, want 29", out.SeverityScore)
	}
}

func TestBlastRadius_VIPAndPremiumUniqueUsers(t *testing.T) {
	s := store.NewStore()
	b := build.NewBuilder()

	// Same VIP user fails twice with same error → should count as 1 VIP user
	for i, tid := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		_ = i
		ev := testutil.MakeEvent(
			testutil.WithTraceID(tid),
			testutil.WithUser("user-vip", "premium", "us"),
			testutil.WithVIP(true),
			testutil.WithError("DUP_ERR", "boom"),
			testutil.WithStatusCode(500),
			testutil.WithService("svc-a"),
		)
		s.Merge(b.Build(ev))
	}

	params, _ := json.Marshal(blastInput{ErrorCode: "DUP_ERR"})
	result, err := handleBlastRadius(context.Background(), s, params)
	if err != nil {
		t.Fatal(err)
	}

	out := result.(blastOutput)

	if out.VIPUsers != 1 {
		t.Errorf("VIPUsers = %d, want 1 (unique)", out.VIPUsers)
	}
	if out.AffectedUsers != 1 {
		t.Errorf("AffectedUsers = %d, want 1 (unique)", out.AffectedUsers)
	}
}

func TestHandleBlastRadius_UsesFlattenedFacts(t *testing.T) {
	now := time.Now()
	store := factStore{
		facts: []store.RequestFacts{
			{
				RequestID:    "req-1",
				SeenAt:       now,
				TraceID:      "trace-1",
				Services:     []string{"checkout"},
				Errors:       []string{"BLAST_ERR"},
				UserID:       "user-vip",
				UserTier:     "premium",
				UserVIP:      true,
				FeatureFlags: []string{"flag-a", "flag-b"},
			},
			{
				RequestID:    "req-2",
				SeenAt:       now.Add(-time.Second),
				TraceID:      "trace-2",
				Services:     []string{"payment"},
				Errors:       []string{"BLAST_ERR"},
				UserID:       "user-standard",
				UserTier:     "standard",
				FeatureFlags: []string{"flag-b"},
			},
		},
		errors: map[string][]string{
			"BLAST_ERR": []string{"req-1", "req-2"},
		},
	}

	params, _ := json.Marshal(blastInput{ErrorCode: "BLAST_ERR", IncludeServices: true, ByTier: true, TopUsers: 5})
	result, err := handleBlastRadius(context.Background(), store, params)
	if err != nil {
		t.Fatal(err)
	}

	out := result.(blastOutput)
	if out.AffectedRequests != 2 {
		t.Fatalf("AffectedRequests = %d, want 2", out.AffectedRequests)
	}
	if out.AffectedUsers != 2 {
		t.Fatalf("AffectedUsers = %d, want 2", out.AffectedUsers)
	}
	if out.VIPUsers != 1 {
		t.Fatalf("VIPUsers = %d, want 1", out.VIPUsers)
	}
	if len(out.FeatureFlags) != 2 || out.FeatureFlags[0] != "flag-a" || out.FeatureFlags[1] != "flag-b" {
		t.Fatalf("FeatureFlags = %+v, want [flag-a flag-b]", out.FeatureFlags)
	}
	if len(out.Services) != 2 {
		t.Fatalf("Services = %+v, want 2 entries", out.Services)
	}
	if len(out.Tiers) != 2 {
		t.Fatalf("Tiers = %+v, want 2 entries", out.Tiers)
	}
	if len(out.TopUsers) != 2 {
		t.Fatalf("TopUsers = %+v, want 2 entries", out.TopUsers)
	}
}

func TestHandleFailures_UsesRequestFactsTraceAndTier(t *testing.T) {
	now := time.Now()
	store := factStore{
		facts: []store.RequestFacts{
			{
				RequestID: "req-1",
				TraceID:   "trace-1",
				SeenAt:    now,
				LatencyMs: 91,
				UserTier:  "premium",
				Errors:    []string{"ERR_A"},
			},
		},
	}

	params, _ := json.Marshal(failuresInput{Tier: "premium"})
	result, err := handleFailures(context.Background(), store, params)
	if err != nil {
		t.Fatal(err)
	}

	out := result.(failuresOutput)
	if out.TotalCount != 1 {
		t.Fatalf("TotalCount = %d, want 1", out.TotalCount)
	}
	if len(out.Failures) != 1 {
		t.Fatalf("Failures len = %d, want 1", len(out.Failures))
	}
	if out.Failures[0].TraceID != "trace-1" {
		t.Fatalf("TraceID = %q, want trace-1", out.Failures[0].TraceID)
	}
	if out.Failures[0].Tier != "premium" {
		t.Fatalf("Tier = %q, want premium", out.Failures[0].Tier)
	}
	if out.Failures[0].ErrorCode != "ERR_A" {
		t.Fatalf("ErrorCode = %q, want ERR_A", out.Failures[0].ErrorCode)
	}
}
