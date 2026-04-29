package cliv2

import (
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

type CapabilitiesResponse struct {
	V2Reads struct {
		Enabled bool `json:"enabled"`
	} `json:"v2_reads"`
}

type EventSearchResponse = apiv2.EventSearchResponse
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

type ErrorsParams struct {
	Window  string
	Service string
	Limit   int
	Cursor  string
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
