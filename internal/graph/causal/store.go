package causal

import "context"

// ClaimQuery filters for ActiveClaims queries.
type ClaimQuery struct {
	ClaimType ClaimType
}

// ClaimStore persists and queries causal claims.
type ClaimStore interface {
	SaveClaims(ctx context.Context, claims []Claim) error
	ActiveClaims(ctx context.Context, q ClaimQuery) ([]Claim, error)
}
