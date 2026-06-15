package incidents

import (
	"fmt"
	"strings"
	"time"
)

// NextCheckContext carries the captured evidence a check can legitimately
// reference. Empty fields mean "Crux does not have this data" — emitters must
// skip any line whose required field is empty rather than printing filler.
// Instrumentation gaps (missing version, missing dep signal) are first-class
// inputs so the panel can suggest closing the gap instead of pretending to
// run a check it cannot run.
type NextCheckContext struct {
	Service   string
	ErrorCode string
	Step      string

	SampleTraceID string

	Downstream      string
	DepSignalID     string
	DepSignalReason string

	DeployVersion   string
	DeployFirstSeen time.Time
	DeploySignalID  string

	RuntimeSignalID string
	RuntimeReason   string
	RuntimeSubtype  string

	AlertSignalID    string
	AlertID          string
	AlertSource      string
	AlertProviderURL string

	MissingServiceVersion   bool
	MissingDependencySignal bool
	HasPartialTrace         bool
}

func NextChecks(cause Cause, confidence Confidence, ctx NextCheckContext) []string {
	var out []string
	switch cause {
	case CauseDeploy:
		out = deployChecks(ctx)
	case CauseDependency:
		out = dependencyChecks(ctx)
	case CauseRuntime:
		out = runtimeChecks(ctx)
	case CauseApp:
		out = appChecks(ctx)
	default:
		out = unknownChecks(ctx)
	}
	out = appendGapChecks(out, ctx)
	if len(out) == 0 {
		out = append(out, "Crux has no evidence-backed checks for this incident yet. Verify ingest is receiving events and signals for this service.")
	}
	return out
}

func deployChecks(ctx NextCheckContext) []string {
	var out []string
	service := backtick(ctx.Service, "")
	if ctx.DeployVersion != "" && !ctx.DeployFirstSeen.IsZero() && service != "" {
		out = append(out, fmt.Sprintf("Compare incident onset with deploy `%s` on %s, first seen at %s.",
			ctx.DeployVersion, service, ctx.DeployFirstSeen.UTC().Format(time.RFC3339)))
	} else if ctx.DeployVersion != "" && service != "" {
		out = append(out, fmt.Sprintf("Compare incident onset with deploy `%s` on %s.", ctx.DeployVersion, service))
	} else if ctx.DeployVersion != "" {
		out = append(out, fmt.Sprintf("Compare incident onset with deploy `%s`.", ctx.DeployVersion))
	}
	if ctx.DeploySignalID != "" {
		if service != "" {
			out = append(out, fmt.Sprintf("Review deploy signal `%s` on %s.", shortRef(ctx.DeploySignalID), service))
		} else {
			out = append(out, fmt.Sprintf("Review deploy signal `%s`.", shortRef(ctx.DeploySignalID)))
		}
	}
	if ctx.SampleTraceID != "" && service != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s` on %s for the deployed-version marker.", shortRef(ctx.SampleTraceID), service))
	}
	if ctx.Downstream != "" {
		out = append(out, fmt.Sprintf("Also verify downstream %s — it was implicated in the same window.", backtick(ctx.Downstream, "")))
	}
	return out
}

func dependencyChecks(ctx NextCheckContext) []string {
	var out []string
	downstream := backtick(ctx.Downstream, "")
	step := backtick(ctx.Step, "")
	if ctx.SampleTraceID != "" && downstream != "" {
		if step != "" {
			out = append(out, fmt.Sprintf("Inspect sampled trace `%s` at %s for the failing call to %s.",
				shortRef(ctx.SampleTraceID), step, downstream))
		} else {
			out = append(out, fmt.Sprintf("Inspect sampled trace `%s` for the failing call to %s.",
				shortRef(ctx.SampleTraceID), downstream))
		}
	}
	if ctx.DepSignalID != "" && ctx.DepSignalReason != "" && downstream != "" {
		out = append(out, fmt.Sprintf("Review dependency signal `%s`: `%s` on %s.",
			shortRef(ctx.DepSignalID), ctx.DepSignalReason, downstream))
	}
	if ctx.DeployVersion != "" {
		line := fmt.Sprintf("Also verify recent deploy %s", backtick(ctx.DeployVersion, ""))
		if ctx.Service != "" {
			line += fmt.Sprintf(" on %s", backtick(ctx.Service, ""))
		}
		out = append(out, line+".")
	}
	if ctx.AlertSignalID != "" {
		out = append(out, alertCheckLine(ctx))
	}
	return out
}

func runtimeChecks(ctx NextCheckContext) []string {
	var out []string
	service := backtick(ctx.Service, "")
	if ctx.RuntimeSignalID != "" && ctx.RuntimeReason != "" && service != "" {
		out = append(out, fmt.Sprintf("Review runtime signal `%s`: `%s` on %s.",
			shortRef(ctx.RuntimeSignalID), ctx.RuntimeReason, service))
	}
	subtype := strings.ToLower(ctx.RuntimeSubtype)
	if service != "" {
		switch {
		case strings.Contains(subtype, "oom") || strings.Contains(subtype, "memory"):
			out = append(out, fmt.Sprintf("Inspect memory usage for %s around the runtime event.", service))
		case strings.Contains(subtype, "readiness") || strings.Contains(subtype, "liveness") || strings.Contains(subtype, "probe"):
			out = append(out, fmt.Sprintf("Review readiness/liveness probe history for %s around the runtime event.", service))
		case strings.Contains(subtype, "crashloop") || strings.Contains(subtype, "restart"):
			out = append(out, fmt.Sprintf("Check %s restart history around the runtime event.", service))
		}
	}
	if ctx.SampleTraceID != "" && service != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s` on %s overlapping the runtime event.",
			shortRef(ctx.SampleTraceID), service))
	}
	if ctx.AlertSignalID != "" {
		out = append(out, alertCheckLine(ctx))
	}
	return out
}

func appChecks(ctx NextCheckContext) []string {
	var out []string
	step := backtick(ctx.Step, "")
	errCode := backtick(ctx.ErrorCode, "")
	if ctx.SampleTraceID != "" && step != "" && errCode != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s` at %s for %s.",
			shortRef(ctx.SampleTraceID), step, errCode))
	} else if ctx.SampleTraceID != "" && step != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s` at %s.",
			shortRef(ctx.SampleTraceID), step))
	} else if ctx.SampleTraceID != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s`.", shortRef(ctx.SampleTraceID)))
	}
	if ctx.AlertSignalID != "" {
		out = append(out, alertCheckLine(ctx))
	}
	return out
}

func unknownChecks(ctx NextCheckContext) []string {
	var out []string
	if ctx.SampleTraceID != "" {
		out = append(out, fmt.Sprintf("Inspect sampled trace `%s` to confirm the failure mode.", shortRef(ctx.SampleTraceID)))
	}
	if ctx.AlertSignalID != "" {
		out = append(out, alertCheckLine(ctx))
	}
	return out
}

// appendGapChecks adds instrumentation-gap suggestions that are independent of
// the classified cause — these are always real (Crux observed the gap).
func appendGapChecks(in []string, ctx NextCheckContext) []string {
	if ctx.MissingServiceVersion && ctx.Service != "" {
		in = append(in, fmt.Sprintf("Add `service.version` to events from `%s` to enable deploy correlation.", ctx.Service))
	}
	if ctx.MissingDependencySignal {
		switch {
		case ctx.Service != "" && ctx.Downstream != "":
			in = append(in, fmt.Sprintf("Add dependency signals for `%s` → `%s` to confirm the downstream cause.", ctx.Service, ctx.Downstream))
		case ctx.Downstream != "":
			in = append(in, fmt.Sprintf("Add dependency signals for `%s` to confirm the downstream cause.", ctx.Downstream))
		case ctx.Service != "":
			in = append(in, fmt.Sprintf("Add dependency signals for `%s` to confirm the downstream cause.", ctx.Service))
		}
	}
	if ctx.HasPartialTrace {
		in = append(in, "Some events arrived without complete span fan-out — verify trace propagation in the SDK middleware.")
	}
	return in
}

func alertCheckLine(ctx NextCheckContext) string {
	id := ctx.AlertID
	if id == "" {
		id = shortRef(ctx.AlertSignalID)
	}
	source := ctx.AlertSource
	switch {
	case ctx.AlertProviderURL != "" && source != "":
		return fmt.Sprintf("Open matched alert `%s` in %s (%s).", id, source, ctx.AlertProviderURL)
	case source != "":
		return fmt.Sprintf("Open matched alert `%s` from %s.", id, source)
	case ctx.AlertProviderURL != "":
		return fmt.Sprintf("Open matched alert `%s` (%s).", id, ctx.AlertProviderURL)
	default:
		return fmt.Sprintf("Open matched alert `%s`.", id)
	}
}

func backtick(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return "`" + value + "`"
}

func shortRef(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}
