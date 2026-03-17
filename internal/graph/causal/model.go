package causal

import "time"

// DeploymentInfo carries the deployment fields needed for causal inference.
// Callers convert from coldstore.Deployment before calling InferIntroducedBy
// to avoid an import cycle (coldstore already imports this package).
type DeploymentInfo struct {
	ID        string
	Service   string
	FirstSeen time.Time
}

// ClaimType identifies the kind of causal inference.
type ClaimType string

const ClaimIntroducedBy ClaimType = "introduced_by"

// ConfidenceTier categorizes confidence into actionable bands.
type ConfidenceTier string

const (
	TierSupported    ConfidenceTier = "supported"
	TierProvisional  ConfidenceTier = "provisional"
	TierInsufficient ConfidenceTier = "insufficient"
)

// Evidence captures the raw signals behind a causal claim.
type Evidence struct {
	BeforeFailures int     `json:"before_failures"`
	AfterFailures  int     `json:"after_failures"`
	Lift           float64 `json:"lift"`
	TimeDeltaMin   float64 `json:"time_delta_min"`
	WindowMinutes  float64 `json:"window_minutes"`
}

// Claim is a single causal inference result.
type Claim struct {
	ClaimType   ClaimType      `json:"claim_type"`
	Subject     string         `json:"subject"`
	Target      string         `json:"target"`
	Service     string         `json:"service"`
	Confidence  float64        `json:"confidence"`
	Tier        ConfidenceTier `json:"tier"`
	Evidence    Evidence       `json:"evidence"`
	WindowStart time.Time      `json:"window_start"`
	WindowEnd   time.Time      `json:"window_end"`
	ShadowMode  bool           `json:"shadow_mode"`
}
