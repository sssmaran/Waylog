package ingestv2

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

type queryError struct {
	code    string
	message string
	detail  string
}

func rejectUnknownParams(q url.Values, allowed map[string]struct{}) *queryError {
	for key := range q {
		if _, ok := allowed[key]; !ok {
			return &queryError{code: errorCodeBadRequest, message: "bad request", detail: "unknown query parameter: " + key}
		}
	}
	return nil
}

func parseLimit(q url.Values) (int, *queryError) {
	raw := q.Get("limit")
	if raw == "" {
		return defaultListLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid limit"}
	}
	if n > maxListLimit {
		return 0, &queryError{code: errorCodeOverLimit, message: "limit exceeds maximum", detail: fmt.Sprintf("limit must be <= %d", maxListLimit)}
	}
	return n, nil
}

func parseIncludeSuppressed(q url.Values) (bool, *queryError) {
	raw := q.Get("include_suppressed")
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid include_suppressed"}
	}
	return v, nil
}

func parseStatusCSV(q url.Values) (map[eventv2.Status]struct{}, *queryError) {
	raw := q.Get("status")
	if raw == "" {
		return nil, nil
	}
	out := map[eventv2.Status]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		status := eventv2.Status(strings.TrimSpace(part))
		switch status {
		case eventv2.StatusOK, eventv2.StatusError, eventv2.StatusTimeout, eventv2.StatusPartial, eventv2.StatusAborted, eventv2.StatusSuppressed:
			out[status] = struct{}{}
		default:
			return nil, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid status"}
		}
	}
	return out, nil
}

func parseTimeWindow(q url.Values, now time.Time, defaultWindow, maxWindow time.Duration) (since, until time.Time, err *queryError) {
	if defaultWindow <= 0 {
		defaultWindow = time.Hour
	}
	if maxWindow <= 0 {
		maxWindow = defaultWindow
	}

	rawSince := q.Get("since")
	rawUntil := q.Get("until")
	if rawSince != "" || rawUntil != "" {
		until = now
		if rawUntil != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, rawUntil)
			if parseErr != nil {
				return time.Time{}, time.Time{}, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid until"}
			}
			until = parsed
		}
		if rawSince != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, rawSince)
			if parseErr != nil {
				return time.Time{}, time.Time{}, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid since"}
			}
			since = parsed
			if since.Before(now.Add(-maxWindow)) {
				return time.Time{}, time.Time{}, &queryError{code: errorCodeOverLimit, message: "time window exceeds maximum", detail: "since is older than the hot window"}
			}
		} else {
			since = until.Add(-maxWindow)
		}
		if since.After(until) {
			return time.Time{}, time.Time{}, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "since must be <= until"}
		}
		return since, until, nil
	}

	window := defaultWindow
	if raw := q.Get("window"); raw != "" {
		parsed, parseErr := parseReadDuration(raw)
		if parseErr != nil || parsed <= 0 {
			return time.Time{}, time.Time{}, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "invalid window"}
		}
		window = parsed
	}
	if window > maxWindow {
		return time.Time{}, time.Time{}, &queryError{code: errorCodeOverLimit, message: "time window exceeds maximum", detail: "window exceeds hot window"}
	}
	until = now
	since = until.Add(-window)
	return since, until, nil
}

func parseReadDuration(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func statusIncludes(statuses map[eventv2.Status]struct{}, status eventv2.Status) bool {
	if len(statuses) == 0 {
		return false
	}
	_, ok := statuses[status]
	return ok
}
