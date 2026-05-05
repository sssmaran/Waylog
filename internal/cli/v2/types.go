package cliv2

import (
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type CapabilitiesResponse struct {
	V2Reads struct {
		Enabled bool `json:"enabled"`
	} `json:"v2_reads"`
	OTLP struct {
		HTTPTraces bool `json:"http_traces"`
	} `json:"otlp"`
}

type EventSearchResponse = apiv2.EventSearchResponse
type Event = eventv2.Event
type RecentTracesResponse = apiv2.RecentTracesResponse
type TraceSummary = apiv2.TraceSummary
type TraceGetResponse = apiv2.TraceGetResponse
type StoryResponse = apiv2.StoryResponse
type StoryAnchor = apiv2.StoryAnchor
type StoryStep = apiv2.StoryStep
type StoryLog = apiv2.StoryLog
type StoryDownstream = apiv2.StoryDownstream
type ErrorFamily = apiv2.ErrorFamily
type ErrorRow = apiv2.ErrorRow
type ErrorsResponse = apiv2.ErrorsResponse
type BlastKey = apiv2.BlastKey
type BlastRadiusResponse = apiv2.BlastRadiusResponse
type Incident = apiv2.Incident
type IncidentEvidence = apiv2.IncidentEvidence
type IncidentListResponse = apiv2.IncidentListResponse
type IncidentDetailResponse = apiv2.IncidentDetailResponse
type IncidentSnapshotResponse = apiv2.IncidentSnapshotResponse

type eventGetResponse struct {
	Event *Event `json:"event"`
}

type ErrorsParams struct {
	Window  string
	Service string
	Limit   int
	Cursor  string
}

type RecentParams struct {
	Window            string
	Service           string
	Status            string
	Limit             int
	Cursor            string
	IncludeSuppressed bool
}

type StoryQuery struct {
	EventID string
	TraceID string
}

type BlastParams struct {
	Service     string
	Step        string
	ErrorCode   string
	ErrorFamily string
	Window      string
}

type SearchParams struct {
	ErrorCode string
	TraceID   string
	Service   string
	Status    string
	Window    string
	Limit     int
	Cursor    string
}

type ClientConfig struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}
