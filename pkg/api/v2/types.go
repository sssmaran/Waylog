// Package apiv2 defines the schema-v2 read API response contracts shared by
// the ingest server and the operator CLI.
package apiv2

import (
	"strings"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	LinkageCausal            = "causal"
	LinkageTimestampFallback = "timestamp_fallback"
	LinkageDirect            = "direct"

	BlastViewSingleFamily = "single_family"
	BlastViewCrossFamily  = "cross_family"
)

type EventSearchResponse struct {
	Events     []*eventv2.Event `json:"events"`
	NextCursor *string          `json:"next_cursor"`
}

type TraceGetResponse struct {
	TraceID string           `json:"trace_id"`
	Events  []*eventv2.Event `json:"events"`
	Linkage string           `json:"linkage"`
}

type TraceSummary struct {
	TraceID       string         `json:"trace_id"`
	TsStart       time.Time      `json:"ts_start"`
	DurationMS    int64          `json:"duration_ms"`
	Services      []string       `json:"services"`
	Status        eventv2.Status `json:"status"`
	AnchorSummary *string        `json:"anchor_summary"`
}

type RecentTracesResponse struct {
	Traces     []TraceSummary `json:"traces"`
	NextCursor *string        `json:"next_cursor"`
}

type StoryResponse struct {
	TraceID    string            `json:"trace_id"`
	Service    string            `json:"service"`
	Route      string            `json:"route"`
	Status     eventv2.Status    `json:"status"`
	Anchor     *StoryAnchor      `json:"anchor"`
	Path       []StoryStep       `json:"path"`
	Logs       []StoryLog        `json:"logs"`
	Downstream []StoryDownstream `json:"downstream"`
	Linkage    string            `json:"linkage"`
}

type StoryAnchor struct {
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
}

type StoryStep struct {
	Name       string             `json:"name"`
	StartMS    int64              `json:"start_ms"`
	DurationMS int64              `json:"duration_ms"`
	Status     eventv2.StepStatus `json:"status"`
	ErrorCode  string             `json:"error_code,omitempty"`
	ErrorMsg   string             `json:"error_msg,omitempty"`
}

type StoryLog struct {
	TsOffsetMS int64            `json:"ts_offset_ms"`
	Level      eventv2.LogLevel `json:"level"`
	Msg        string           `json:"msg"`
	Step       string           `json:"step,omitempty"`
}

type StoryDownstream struct {
	Step     string `json:"step"`
	Service  string `json:"service"`
	Endpoint string `json:"endpoint"`
}

type ErrorFamily struct {
	Service   string `json:"service"`
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
}

type ErrorRow struct {
	ErrorFamily    ErrorFamily `json:"error_family"`
	Count          int         `json:"count"`
	AffectedUsers  *int        `json:"affected_users"`
	AffectedTraces int         `json:"affected_traces"`
	SampleTraces   []string    `json:"sample_traces"`
}

type ErrorsResponse struct {
	Window     string     `json:"window"`
	Rows       []ErrorRow `json:"rows"`
	NextCursor *string    `json:"next_cursor"`
}

type BlastKey struct {
	Service   string `json:"service,omitempty"`
	Step      string `json:"step,omitempty"`
	ErrorCode string `json:"error_code"`
}

type BlastRadiusResponse struct {
	Key              BlastKey `json:"key"`
	ViewMode         string   `json:"view_mode"`
	Window           string   `json:"window"`
	AffectedRequests int      `json:"affected_requests"`
	AffectedUsers    *int     `json:"affected_users"`
	AffectedServices int      `json:"affected_services"`
	TopServices      []string `json:"top_services"`
	SampleTraces     []string `json:"sample_traces"`
}

func FormatErrorFamily(f ErrorFamily) string {
	return escapeErrorFamilyPart(f.Service) + ":" + escapeErrorFamilyPart(f.Step) + ":" + escapeErrorFamilyPart(f.ErrorCode)
}

func ParseErrorFamily(s string) (BlastKey, bool) {
	parts := make([]string, 0, 3)
	var current []rune
	escaped := false
	for _, r := range s {
		if escaped {
			if r != ':' && r != '\\' {
				return BlastKey{}, false
			}
			current = append(current, r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case ':':
			parts = append(parts, string(current))
			current = current[:0]
		default:
			current = append(current, r)
		}
	}
	if escaped {
		return BlastKey{}, false
	}
	parts = append(parts, string(current))
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return BlastKey{}, false
	}
	return BlastKey{Service: parts[0], Step: parts[1], ErrorCode: parts[2]}, true
}

func escapeErrorFamilyPart(s string) string {
	return strings.ReplaceAll(s, ":", `\:`)
}
