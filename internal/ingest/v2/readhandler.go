package ingestv2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	errorCodeBadRequest       = "bad_request"
	errorCodeNotFound         = "not_found"
	errorCodeOverLimit        = "over_limit"
	errorCodeValidationFailed = "validation_failed"
	errorCodeUnavailable      = "unavailable"

	readHandlerEventGet     = "event_get"
	readHandlerEventSearch  = "event_search"
	readHandlerTraceGet     = "trace_get"
	readHandlerTracesRecent = "traces_recent"
	readHandlerTraceStory   = "trace_story"
	readHandlerErrors       = "errors"
	readHandlerBlastRadius  = "blast_radius"
)

type ReadHandler struct {
	reader    *Reader
	metrics   *metrics.Metrics
	maxWindow time.Duration
	now       func() time.Time
}

func NewReadHandler(reader *Reader, m *metrics.Metrics, maxWindow time.Duration) *ReadHandler {
	return &ReadHandler{reader: reader, metrics: m, maxWindow: maxWindow, now: time.Now}
}

func (h *ReadHandler) EventByID(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerEventGet, func() {
		if !requireGET(w, r) {
			return
		}
		if err := rejectUnknownParams(r.URL.Query(), nil); err != nil {
			writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
			return
		}
		eventID := singlePathTail(r.URL.Path, "/v1/events/")
		if eventID == "" {
			h.recordNotFound(readHandlerEventGet)
			writeReadError(w, http.StatusNotFound, errorCodeNotFound, "event not found", "")
			return
		}
		ev, ok := h.reader.GetEvent(eventID)
		if !ok {
			h.recordNotFound(readHandlerEventGet)
			writeReadError(w, http.StatusNotFound, errorCodeNotFound, "event not found", "")
			return
		}
		writeReadJSON(w, http.StatusOK, eventGetResponse{Event: ev})
	})
}

func (h *ReadHandler) EventSearch(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerEventSearch, func() {
		if !requireGET(w, r) {
			return
		}
		params, ok := h.parseSearchParams(w, r, time.Hour, allowedEventSearchParams, true)
		if !ok {
			return
		}
		result := h.reader.SearchEvents(params.Filter, params.EventCursor, params.Limit)
		if len(result.Events) == 0 {
			h.recordEmpty(readHandlerEventSearch)
		}
		next, err := encodeOptionalEventCursor(result.NextCursor)
		if err != nil {
			writeReadError(w, http.StatusInternalServerError, errorCodeUnavailable, "unavailable", "")
			return
		}
		writeReadJSON(w, http.StatusOK, eventSearchResponse{Events: result.Events, NextCursor: next})
	})
}

func (h *ReadHandler) TraceByID(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerTraceGet, func() {
		if !requireGET(w, r) {
			return
		}
		if err := rejectUnknownParams(r.URL.Query(), nil); err != nil {
			writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
			return
		}
		traceID := singlePathTail(r.URL.Path, "/v1/traces/")
		if traceID == "" {
			h.recordNotFound(readHandlerTraceGet)
			writeReadError(w, http.StatusNotFound, errorCodeNotFound, "trace not found", "")
			return
		}
		result, ok := h.reader.GetTrace(traceID)
		if !ok {
			h.recordNotFound(readHandlerTraceGet)
			writeReadError(w, http.StatusNotFound, errorCodeNotFound, "trace not found", "")
			return
		}
		writeReadJSON(w, http.StatusOK, traceGetResponse(result))
	})
}

func (h *ReadHandler) RecentTraces(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerTracesRecent, func() {
		if !requireGET(w, r) {
			return
		}
		params, ok := h.parseSearchParams(w, r, 24*time.Hour, allowedRecentTracesParams, false)
		if !ok {
			return
		}
		result := h.reader.RecentTraces(params.Filter, params.TraceCursor, params.Limit)
		if len(result.Traces) == 0 {
			h.recordEmpty(readHandlerTracesRecent)
		}
		next, err := encodeOptionalTraceCursor(result.NextCursor)
		if err != nil {
			writeReadError(w, http.StatusInternalServerError, errorCodeUnavailable, "unavailable", "")
			return
		}
		writeReadJSON(w, http.StatusOK, recentTracesResponse{Traces: result.Traces, NextCursor: next})
	})
}

func (h *ReadHandler) TraceStory(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerTraceStory, func() {
		if !requireGET(w, r) {
			return
		}
		q := r.URL.Query()
		if err := rejectUnknownParams(q, allowedTraceStoryParams); err != nil {
			writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
			return
		}
		eventID, traceID := q.Get("event_id"), q.Get("trace_id")
		if (eventID == "") == (traceID == "") {
			writeReadError(w, http.StatusBadRequest, errorCodeBadRequest, "bad request", "exactly one of event_id or trace_id is required")
			return
		}
		var story StoryResponse
		var ok bool
		if eventID != "" {
			story, ok = h.reader.TraceStoryByEventID(eventID)
		} else {
			story, ok = h.reader.TraceStoryByTraceID(traceID)
		}
		if !ok {
			h.recordNotFound(readHandlerTraceStory)
			writeReadError(w, http.StatusNotFound, errorCodeNotFound, "trace story not found", "")
			return
		}
		writeReadJSON(w, http.StatusOK, story)
	})
}

func (h *ReadHandler) Errors(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerErrors, func() {
		if !requireGET(w, r) {
			return
		}
		params, ok := h.parseErrorsParams(w, r)
		if !ok {
			return
		}
		result := h.reader.Errors(params.Filter, params.ErrorCursor, params.Limit)
		if len(result.Rows) == 0 {
			h.recordEmpty(readHandlerErrors)
		}
		next, err := encodeOptionalErrorCursor(result.NextCursor)
		if err != nil {
			writeReadError(w, http.StatusInternalServerError, errorCodeUnavailable, "unavailable", "")
			return
		}
		rows := result.Rows
		if rows == nil {
			rows = []ErrorRow{}
		}
		writeReadJSON(w, http.StatusOK, errorsResponse{Window: result.Window, Rows: rows, NextCursor: next})
	})
}

func (h *ReadHandler) BlastRadius(w http.ResponseWriter, r *http.Request) {
	h.observe(readHandlerBlastRadius, func() {
		if !requireGET(w, r) {
			return
		}
		q := r.URL.Query()
		if err := rejectUnknownParams(q, allowedBlastRadiusParams); err != nil {
			writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
			return
		}
		now := h.now()
		since, until, qerr := parseTimeWindow(q, now, time.Hour, h.maxWindow)
		if qerr != nil {
			writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
			return
		}
		key, ok := parseBlastKey(q)
		if !ok {
			writeReadError(w, http.StatusBadRequest, errorCodeBadRequest, "bad request", "invalid error family key")
			return
		}
		result := h.reader.BlastRadius(SearchFilter{Since: since, Until: until}, key)
		if result.AffectedRequests == 0 {
			h.recordEmpty(readHandlerBlastRadius)
		}
		writeReadJSON(w, http.StatusOK, result)
	})
}

type parsedReadParams struct {
	Filter      SearchFilter
	EventCursor *EventCursor
	TraceCursor *TraceCursor
	ErrorCursor *ErrorCursor
	Limit       int
}

func (h *ReadHandler) parseSearchParams(w http.ResponseWriter, r *http.Request, defaultWindow time.Duration, allowed map[string]struct{}, eventCursor bool) (parsedReadParams, bool) {
	q := r.URL.Query()
	if err := rejectUnknownParams(q, allowed); err != nil {
		writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
		return parsedReadParams{}, false
	}
	limit, qerr := parseLimit(q)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	includeSuppressed, qerr := parseIncludeSuppressed(q)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	statuses, qerr := parseStatusCSV(q)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	now := h.now()
	since, until, qerr := parseTimeWindow(q, now, defaultWindow, h.maxWindow)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	params := parsedReadParams{
		Limit: limit,
		Filter: SearchFilter{
			Service:           q.Get("service"),
			Statuses:          statuses,
			ErrorCode:         q.Get("error_code"),
			TraceID:           q.Get("trace_id"),
			Since:             since,
			Until:             until,
			IncludeSuppressed: includeSuppressed || statusIncludes(statuses, eventv2.StatusSuppressed),
		},
	}
	if raw := q.Get("cursor"); raw != "" {
		if eventCursor {
			cursor, err := DecodeEventCursor(raw)
			if err != nil {
				writeReadError(w, http.StatusBadRequest, errorCodeBadRequest, "bad request", "invalid cursor")
				return parsedReadParams{}, false
			}
			params.EventCursor = &cursor
		} else {
			cursor, err := DecodeTraceCursor(raw)
			if err != nil {
				writeReadError(w, http.StatusBadRequest, errorCodeBadRequest, "bad request", "invalid cursor")
				return parsedReadParams{}, false
			}
			params.TraceCursor = &cursor
		}
	}
	return params, true
}

func (h *ReadHandler) parseErrorsParams(w http.ResponseWriter, r *http.Request) (parsedReadParams, bool) {
	q := r.URL.Query()
	if err := rejectUnknownParams(q, allowedErrorsParams); err != nil {
		writeReadError(w, http.StatusBadRequest, err.code, err.message, err.detail)
		return parsedReadParams{}, false
	}
	limit, qerr := parseLimit(q)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	statuses, qerr := parseErrorStatuses(q)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	since, until, qerr := parseTimeWindow(q, h.now(), time.Hour, h.maxWindow)
	if qerr != nil {
		writeReadError(w, http.StatusBadRequest, qerr.code, qerr.message, qerr.detail)
		return parsedReadParams{}, false
	}
	params := parsedReadParams{
		Limit: limit,
		Filter: SearchFilter{
			Service:  q.Get("service"),
			Statuses: statuses,
			Since:    since,
			Until:    until,
		},
	}
	if raw := q.Get("cursor"); raw != "" {
		cursor, err := DecodeErrorCursor(raw)
		if err != nil {
			writeReadError(w, http.StatusBadRequest, errorCodeBadRequest, "bad request", "invalid cursor")
			return parsedReadParams{}, false
		}
		params.ErrorCursor = &cursor
	}
	return params, true
}

type eventGetResponse struct {
	Event *eventv2.Event `json:"event"`
}

type eventSearchResponse struct {
	Events     []*eventv2.Event `json:"events"`
	NextCursor *string          `json:"next_cursor"`
}

type traceGetResponse struct {
	TraceID string           `json:"trace_id"`
	Events  []*eventv2.Event `json:"events"`
	Linkage string           `json:"linkage"`
}

type recentTracesResponse struct {
	Traces     []TraceSummary `json:"traces"`
	NextCursor *string        `json:"next_cursor"`
}

type errorsResponse struct {
	Window     string     `json:"window"`
	Rows       []ErrorRow `json:"rows"`
	NextCursor *string    `json:"next_cursor"`
}

type readErrorBody struct {
	Error readErrorDetail `json:"error"`
}

type readErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

var allowedEventSearchParams = map[string]struct{}{
	"service": {}, "status": {}, "error_code": {}, "trace_id": {}, "since": {}, "until": {}, "window": {}, "include_suppressed": {}, "cursor": {}, "limit": {},
}

var allowedRecentTracesParams = map[string]struct{}{
	"service": {}, "status": {}, "since": {}, "until": {}, "window": {}, "include_suppressed": {}, "cursor": {}, "limit": {},
}

var allowedTraceStoryParams = map[string]struct{}{
	"event_id": {}, "trace_id": {},
}

var allowedErrorsParams = map[string]struct{}{
	"service": {}, "status": {}, "since": {}, "until": {}, "window": {}, "cursor": {}, "limit": {},
}

var allowedBlastRadiusParams = map[string]struct{}{
	"service": {}, "step": {}, "error_code": {}, "error_family": {}, "since": {}, "until": {}, "window": {},
}

func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func singlePathTail(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	tail := strings.TrimPrefix(path, prefix)
	if tail == "" || strings.Contains(tail, "/") {
		return ""
	}
	return tail
}

func writeReadJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeReadError(w http.ResponseWriter, status int, code, message, detail string) {
	writeReadJSON(w, status, readErrorBody{Error: readErrorDetail{Code: code, Message: message, Detail: detail}})
}

func encodeOptionalEventCursor(cursor *EventCursor) (*string, error) {
	if cursor == nil {
		return nil, nil
	}
	encoded, err := EncodeEventCursor(*cursor)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func encodeOptionalTraceCursor(cursor *TraceCursor) (*string, error) {
	if cursor == nil {
		return nil, nil
	}
	encoded, err := EncodeTraceCursor(*cursor)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func encodeOptionalErrorCursor(cursor *ErrorCursor) (*string, error) {
	if cursor == nil {
		return nil, nil
	}
	encoded, err := EncodeErrorCursor(*cursor)
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

func parseErrorStatuses(q url.Values) (map[eventv2.Status]struct{}, *queryError) {
	statuses, err := parseStatusCSV(q)
	if err != nil {
		return nil, err
	}
	for status := range statuses {
		if !isFailedStatus(status) {
			return nil, &queryError{code: errorCodeBadRequest, message: "bad request", detail: "status must be one of error, timeout, partial, aborted"}
		}
	}
	return statuses, nil
}

func parseBlastKey(q url.Values) (BlastKeyMode, bool) {
	service, step, code := q.Get("service"), q.Get("step"), q.Get("error_code")
	if service != "" || step != "" {
		if service == "" || step == "" || code == "" {
			return BlastKeyMode{}, false
		}
		return BlastKeyMode{Key: BlastKey{Service: service, Step: step, ErrorCode: code}}, true
	}
	if display := q.Get("error_family"); display != "" {
		key, ok := parseErrorFamilyDisplay(display)
		if !ok {
			return BlastKeyMode{}, false
		}
		return BlastKeyMode{Key: key}, true
	}
	if code == "" {
		return BlastKeyMode{}, false
	}
	return BlastKeyMode{Key: BlastKey{ErrorCode: code}, CrossCode: true}, true
}

func (h *ReadHandler) observe(handler string, fn func()) {
	start := time.Now()
	defer func() {
		if h.metrics != nil {
			h.metrics.V2ReadLatency.WithLabelValues(handler).Observe(time.Since(start).Seconds())
		}
	}()
	fn()
}

func (h *ReadHandler) recordEmpty(handler string) {
	if h.metrics != nil {
		h.metrics.V2ReadEmpty.WithLabelValues(handler).Inc()
	}
}

func (h *ReadHandler) recordNotFound(handler string) {
	if h.metrics != nil {
		h.metrics.V2ReadNotFound.WithLabelValues(handler).Inc()
	}
}
