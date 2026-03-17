package causal

import "context"

// ClaimStore persists and queries causal claims.
type ClaimStore interface {
	SaveClaims(ctx context.Context, claims []Claim) error
	ActiveClaims(ctx context.Context, claimType ClaimType) ([]Claim, error)
}
