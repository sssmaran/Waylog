package detect

import "time"

// Insight is the structured output of a detected anomaly.
// Built by chaining window comparison, blast radius, and deploy correlation.
type Insight struct {
	DetectedAt        time.Time          `json:"detected_at"`
	TopErrorCode      string             `json:"top_error_code"`
	Lift              float64            `json:"lift"`
	CurrentCount      int                `json:"current_count"`
	BaselineCount     int                `json:"baseline_count"`
	AffectedRequests  int                `json:"affected_requests"`
	AffectedUsers     int                `json:"affected_users"`
	VIPUsers          int                `json:"vip_users"`
	Services          []string           `json:"services"`
	SeverityScore     float64            `json:"severity_score"`
	DeployCorrelation *DeployCorrelation `json:"deploy_correlation,omitempty"`
}

type DeployCorrelation struct {
	DeploymentID string  `json:"deployment_id"`
	Service      string  `json:"service"`
	Confidence   float64 `json:"confidence"`
}
