package waylogv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ExplainResult is the canonical narrative shape (§4.1). String() renders the
// plain-text form used by the CLI and dev stderr; the same structure is
// serialized to JSON by GET /v1/traces/story (slice 3+).
type ExplainResult struct {
	TraceID    string
	Service    string
	Route      string
	Status     string
	Anchor     AnchorInfo
	Path       []StepInfo
	Logs       []LogInfo
	Downstream []DownstreamEdge
}

type AnchorInfo struct {
	Step      string
	ErrorCode string
}

type StepInfo struct {
	Name       string
	StartMS    int64
	DurationMS int64
	Status     string
	ErrorCode  string
	ErrorMsg   string
}

type LogInfo struct {
	TsOffsetMS int64
	Level      string
	Msg        string
	Step       string
}

type DownstreamEdge struct {
	Step     string
	Service  string
	Endpoint string
}

// ErrNoActiveRequest is returned when Explain is called outside a request.
var ErrNoActiveRequest = errors.New("waylog: no active request")

// Explain returns a snapshot view of the in-flight request buffer. It does
// not seal or finalize the request.
func Explain(ctx context.Context) (*ExplainResult, error) {
	r := requestFromContext(ctx)
	if r == nil {
		return nil, ErrNoActiveRequest
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	out := &ExplainResult{
		TraceID: r.traceID,
		Service: r.sdk.cfg.Service,
		Status:  string(r.snapshotStatusLocked()),
	}
	if route, ok := routeFromFields(r.fields); ok {
		out.Route = route
	}
	if r.anchorStep != "" || r.anchorCode != "" {
		out.Anchor = AnchorInfo{Step: r.anchorStep, ErrorCode: r.anchorCode}
	}
	for _, s := range r.steps {
		si := StepInfo{
			Name:       s.name,
			StartMS:    s.startMS,
			DurationMS: s.durationMS,
			Status:     s.status,
		}
		if s.err != nil {
			si.ErrorCode = s.err.Code
			si.ErrorMsg = s.err.Reason
		}
		out.Path = append(out.Path, si)
		if s.downstream != nil {
			out.Downstream = append(out.Downstream, DownstreamEdge{
				Step:     s.name,
				Service:  s.downstream.Service,
				Endpoint: s.downstream.Endpoint,
			})
		}
	}
	for _, l := range r.logs {
		out.Logs = append(out.Logs, LogInfo{
			TsOffsetMS: l.tsOffsetMS,
			Level:      l.level,
			Msg:        l.msg,
			Step:       l.stepName,
		})
	}
	return out, nil
}

func (r *ExplainResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "trace=%s service=%s status=%s\n", r.TraceID, r.Service, r.Status)
	if r.Route != "" {
		fmt.Fprintf(&b, "route=%s\n", r.Route)
	}
	if r.Anchor.Step != "" || r.Anchor.ErrorCode != "" {
		fmt.Fprintf(&b, "anchor=%s code=%s\n", r.Anchor.Step, r.Anchor.ErrorCode)
	}
	for _, s := range r.Path {
		if s.Status == "error" {
			fmt.Fprintf(&b, "  ✗ %s (%dms) %s: %s\n", s.Name, s.DurationMS, s.ErrorCode, s.ErrorMsg)
			continue
		}
		fmt.Fprintf(&b, "  · %s (%dms)\n", s.Name, s.DurationMS)
	}
	for _, l := range r.Logs {
		if l.Step != "" {
			fmt.Fprintf(&b, "[%s] [%dms] [%s] %s\n", l.Level, l.TsOffsetMS, l.Step, l.Msg)
			continue
		}
		fmt.Fprintf(&b, "[%s] [%dms] %s\n", l.Level, l.TsOffsetMS, l.Msg)
	}
	if len(r.Downstream) > 0 {
		b.WriteString("downstream:\n")
		for _, d := range r.Downstream {
			fmt.Fprintf(&b, "  %s -> %s (%s)\n", d.Step, d.Service, d.Endpoint)
		}
	}
	return b.String()
}

func routeFromFields(f F) (string, bool) {
	httpMap, ok := f["http"].(map[string]any)
	if !ok {
		return "", false
	}
	r, _ := httpMap["route"].(string)
	return r, r != ""
}
