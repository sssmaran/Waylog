package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

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
