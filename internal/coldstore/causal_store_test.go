package coldstore

import (
	"context"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/causal"
)

func TestCausalStore_SaveAndQuery(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	claims := []causal.Claim{
		{
			ClaimType:   causal.ClaimIntroducedBy,
			Subject:     "PMT_502",
			Target:      "deploy_abc",
			Service:     "payment-service",
			Confidence:  0.92,
			Tier:        causal.TierSupported,
			Evidence:    causal.Evidence{BeforeFailures: 2, AfterFailures: 100, Lift: 50, TimeDeltaMin: 5, WindowMinutes: 30},
			WindowStart: now.Add(-30 * time.Minute),
			WindowEnd:   now,
			ShadowMode:  true,
		},
	}

	if err := s.SaveClaims(ctx, claims); err != nil {
		t.Fatal("SaveClaims:", err)
	}

	active, err := s.ActiveClaims(ctx, causal.ClaimIntroducedBy)
	if err != nil {
		t.Fatal("ActiveClaims:", err)
	}
	if len(active) != 1 {
		t.Fatalf("got %d active claims, want 1", len(active))
	}
	if active[0].Subject != "PMT_502" {
		t.Errorf("subject = %q, want PMT_502", active[0].Subject)
	}
	if active[0].Confidence != 0.92 {
		t.Errorf("confidence = %f, want 0.92", active[0].Confidence)
	}
}

func TestCausalStore_SupersedesOldClaims(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	old := []causal.Claim{
		{
			ClaimType:   causal.ClaimIntroducedBy,
			Subject:     "PMT_502",
			Target:      "deploy_old",
			Service:     "payment-service",
			Confidence:  0.80,
			Tier:        causal.TierProvisional,
			Evidence:    causal.Evidence{Lift: 5},
			WindowStart: now.Add(-60 * time.Minute),
			WindowEnd:   now.Add(-30 * time.Minute),
			ShadowMode:  true,
		},
	}
	if err := s.SaveClaims(ctx, old); err != nil {
		t.Fatal(err)
	}

	updated := []causal.Claim{
		{
			ClaimType:   causal.ClaimIntroducedBy,
			Subject:     "PMT_502",
			Target:      "deploy_new",
			Service:     "payment-service",
			Confidence:  0.92,
			Tier:        causal.TierSupported,
			Evidence:    causal.Evidence{Lift: 50},
			WindowStart: now.Add(-30 * time.Minute),
			WindowEnd:   now,
			ShadowMode:  true,
		},
	}
	if err := s.SaveClaims(ctx, updated); err != nil {
		t.Fatal(err)
	}

	active, err := s.ActiveClaims(ctx, causal.ClaimIntroducedBy)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("got %d active claims, want 1 (old should be superseded)", len(active))
	}
	if active[0].Target != "deploy_new" {
		t.Errorf("target = %q, want deploy_new", active[0].Target)
	}
}

func TestCausalStore_EmptyResult(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	active, err := s.ActiveClaims(context.Background(), causal.ClaimIntroducedBy)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("got %d claims, want 0", len(active))
	}
}
