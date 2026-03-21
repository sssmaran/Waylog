package waylog

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

func (c *Client) assembleEvent(
	ctx context.Context,
	statusCode int,
	latencyMs int64,
	err error,
	callerService string,
	downstreamService string,
) event.WideEvent {
	if statusCode <= 0 {
		statusCode = http.StatusOK
	}

	success := statusCode < http.StatusInternalServerError
	if err != nil {
		success = false
	}

	eventName := c.cfg.Service + ".request"
	if !success {
		eventName = c.cfg.Service + ".error"
	}

	user := event.UserContext{ID: "system"}
	if ctxUser, ok := userFromContext(ctx); ok {
		if ctxUser.ID != "" {
			user.ID = ctxUser.ID
		}
		user.Tier = ctxUser.Tier
		user.Region = ctxUser.Region
		user.VIP = ctxUser.VIP
	}

	flow, _ := flowFromContext(ctx)
	flags, _ := flagsFromContext(ctx)
	httpMethod, _ := httpMethodFromContext(ctx)
	routeTemplate, _ := routeTemplateFromContext(ctx)

	tc, _ := trace.FromContext(ctx)
	traceID := tc.TraceID
	spanID := tc.SpanID
	parentSpanID := tc.ParentSpanID
	if traceID == "" {
		traceID = trace.NewTraceID()
	}
	if spanID == "" {
		spanID = trace.NewSpanID()
	}

	system := event.SystemContext{
		Service:           c.cfg.Service,
		Version:           c.cfg.Version,
		DeploymentID:      c.cfg.DeploymentID,
		Env:               c.cfg.Env,
		CallerService:     callerService,
		DownstreamService: downstreamService,
	}
	if system.CallerService == "" {
		system.CallerService = "external"
	}

	outcome := event.OutcomeContext{
		Success:    success,
		StatusCode: statusCode,
		Kind:       "http",
	}

	var errContext *event.ErrorContext
	if !success {
		code := c.classifyError(err, statusCode)
		message := errorMessage(err, statusCode)
		errContext = &event.ErrorContext{
			Code:    code,
			Message: message,
		}
	}

	return event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     eventName,
		Timestamp:     time.Now().UTC(),

		User: user,
		Request: event.RequestContext{
			TraceID:       traceID,
			SpanID:        spanID,
			ParentSpanID:  parentSpanID,
			HTTPMethod:    httpMethod,
			RouteTemplate: routeTemplate,
			Flow:          flow,
			FeatureFlags:  flags,
		},
		System:  system,
		Outcome: outcome,
		Error:   errContext,
		Metrics: event.MetricsContext{
			LatencyMs: latencyMs,
		},
	}
}

func (c *Client) classifyError(err error, statusCode int) string {
	if err != nil && c.cfg.ErrorClassifier != nil {
		if code := c.cfg.ErrorClassifier(err); code != "" {
			return code
		}
	}
	if statusCode > 0 {
		return fmt.Sprintf("HTTP_%d", statusCode)
	}
	return "UNKNOWN"
}

func errorMessage(err error, statusCode int) string {
	if err != nil {
		return err.Error()
	}
	message := http.StatusText(statusCode)
	if message == "" {
		return "error"
	}
	return message
}
