package incidents

import (
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

type Status string

const (
	StatusActive     Status = "active"
	StatusRecovering Status = "recovering"
	StatusResolved   Status = "resolved"
)

type Cause string

const (
	CauseDeploy     Cause = "deploy"
	CauseApp        Cause = "app"
	CauseDependency Cause = "dependency"
	CauseRuntime    Cause = "runtime"
	CauseUnknown    Cause = "unknown"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type EvidenceKind string

const (
	EvidenceSignal      EvidenceKind = "signal"
	EvidenceDeployment  EvidenceKind = "deployment"
	EvidenceTrace       EvidenceKind = "trace"
	EvidenceMetric      EvidenceKind = "metric"
	EvidencePropagation EvidenceKind = "propagation"
	EvidenceBlast       EvidenceKind = "blast"
)

type EvidenceCaptureStatus string

const (
	CaptureOK      EvidenceCaptureStatus = "ok"
	CapturePartial EvidenceCaptureStatus = "partial"
	CaptureMissing EvidenceCaptureStatus = "missing"
)

type PropagationStep struct {
	Service    string `json:"service"`
	Step       string `json:"step"`
	StartMS    int64  `json:"start_ms"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type PropagationEvidence struct {
	OriginService string                `json:"origin_service"`
	OriginStep    string                `json:"origin_step"`
	Path          []PropagationStep     `json:"path"`
	SampleTraceID string                `json:"sample_trace_id"`
	FirstSeenAt   *time.Time            `json:"first_seen_at,omitempty"`
	CapturedAt    time.Time             `json:"captured_at"`
	CaptureStatus EvidenceCaptureStatus `json:"capture_status"`
}

type BlastEvidence struct {
	AffectedRequests int                   `json:"affected_requests"`
	AffectedUsers    *int                  `json:"affected_users,omitempty"`
	AffectedServices int                   `json:"affected_services"`
	TopServices      []string              `json:"top_services"`
	SampledTraces    []string              `json:"sampled_traces"`
	CapturedAt       time.Time             `json:"captured_at"`
	CaptureStatus    EvidenceCaptureStatus `json:"capture_status"`
}

type PropagationSnapshot struct {
	Opening *PropagationEvidence `json:"opening,omitempty"`
	Latest  *PropagationEvidence `json:"latest,omitempty"`
}

type BlastSnapshot struct {
	Opening *BlastEvidence `json:"opening,omitempty"`
	Latest  *BlastEvidence `json:"latest,omitempty"`
}

type Evidence struct {
	Kind       EvidenceKind   `json:"kind"`
	Title      string         `json:"title"`
	Detail     string         `json:"detail,omitempty"`
	Service    string         `json:"service,omitempty"`
	SignalID   string         `json:"signal_id,omitempty"`
	DeployID   string         `json:"deployment_id,omitempty"`
	TraceID    string         `json:"trace_id,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Incident struct {
	IncidentID              string               `json:"incident_id"`
	Env                     string               `json:"env"`
	Service                 string               `json:"service"`
	ErrorFamily             apiv2.ErrorFamily    `json:"error_family"`
	Status                  Status               `json:"status"`
	Cause                   Cause                `json:"cause"`
	Confidence              Confidence           `json:"confidence"`
	Severity                int                  `json:"severity"`
	StartedAt               time.Time            `json:"started_at"`
	UpdatedAt               time.Time            `json:"updated_at"`
	LastSeenAt              time.Time            `json:"last_seen_at"`
	RecoveringAt            *time.Time           `json:"recovering_at,omitempty"`
	ResolvedAt              *time.Time           `json:"resolved_at,omitempty"`
	AffectedRequests        int                  `json:"affected_requests"`
	AffectedUsers           *int                 `json:"affected_users,omitempty"`
	AffectedServices        int                  `json:"affected_services"`
	TopServices             []string             `json:"top_services"`
	SampleTraces            []string             `json:"sample_traces"`
	Evidence                []Evidence           `json:"evidence"`
	NextChecks              []string             `json:"next_checks"`
	InstrumentationWarnings []string             `json:"instrumentation_warnings,omitempty"`
	Lift                    float64              `json:"lift"`
	BaselineCount           int                  `json:"baseline_count"`
	CurrentCount            int                  `json:"current_count"`
	Propagation             *PropagationSnapshot `json:"propagation,omitempty"`
	Blast                   *BlastSnapshot       `json:"blast,omitempty"`
}

type Deployment struct {
	ID        string
	Service   string
	Version   string
	Env       string
	FirstSeen time.Time
	LastSeen  time.Time
	Metadata  map[string]string
}
