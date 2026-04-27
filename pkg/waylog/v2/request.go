package waylogv2

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const (
	stepStatusOK    = "ok"
	stepStatusError = "error"
)

type ctxKey struct{}

// BeginOptions seeds a new request buffer with identity captured by the
// caller (HTTP middleware, test code, future framework adapters).
type BeginOptions struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Now          time.Time
}

// Begin opens a request buffer and returns a context that carries it. The
// returned ctx must be passed to Finalize to assemble and emit the final
// wide event.
//
// Begin is intended for middleware and adapter authors. Application code
// should use a framework adapter once those exist.
func Begin(ctx context.Context, opts BeginOptions) context.Context {
	s := getState()
	if s == nil {
		return ctx
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	traceID := opts.TraceID
	if traceID == "" {
		traceID = newTraceID()
	}
	spanID := opts.SpanID
	if spanID == "" {
		spanID = newSpanID()
	}

	r := &request{
		sdk:          s,
		eventID:      newEventID(),
		tsStart:      now,
		traceID:      traceID,
		spanID:       spanID,
		parentSpanID: opts.ParentSpanID,
		fields:       F{},
		maxSteps:     s.cfg.MaxSteps,
		maxLogs:      s.cfg.MaxLogs,
		maxBytes:     s.cfg.MaxBufferBytes,
	}

	s.mu.Lock()
	s.active[r] = struct{}{}
	s.mu.Unlock()

	return context.WithValue(ctx, ctxKey{}, r)
}

func requestFromContext(ctx context.Context) *request {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(ctxKey{}).(*request)
	return v
}

// SetField sets a top-level request field (e.g. fields.http or fields.user).
// The value is deep-cloned for `map[string]any` and `[]any` shapes, so caller
// mutation at any level after the call cannot change the emitted event.
// Logger field bags (`From(ctx).Info(msg, F{...})`) use a shallower contract.
func SetField(ctx context.Context, key string, value any) {
	r := requestFromContext(ctx)
	if r == nil || key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed || r.sealed {
		return
	}
	r.fields[key] = cloneDeep(value)
}

// SetHTTPStatus updates fields.http.status on the active request. Intended for
// HTTP middleware and adapter authors.
func SetHTTPStatus(ctx context.Context, status int) {
	r := requestFromContext(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed || r.sealed {
		return
	}
	r.setHTTPStatusLocked(status)
}

// SetHTTPRoute updates fields.http.route on the active request. Intended for
// HTTP middleware and adapter authors that resolve route templates late.
func SetHTTPRoute(ctx context.Context, route string) {
	r := requestFromContext(ctx)
	if r == nil || route == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed || r.sealed {
		return
	}
	httpFields, _ := r.fields["http"].(map[string]any)
	if httpFields == nil {
		httpFields = map[string]any{}
		r.fields["http"] = httpFields
	}
	httpFields["route"] = route
}

// TraceID returns the request trace id bound to ctx, or empty when absent.
func TraceID(ctx context.Context) string {
	r := requestFromContext(ctx)
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.traceID
}

// SpanID returns the local server span id bound to ctx, or empty when absent.
func SpanID(ctx context.Context) string {
	r := requestFromContext(ctx)
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spanID
}

// RecordOutgoingSpan attaches a client span id and downstream edge to the
// innermost active step so the emitted event can be linked to child events.
func RecordOutgoingSpan(ctx context.Context, clientSpan, downstreamService, endpoint string) {
	r := requestFromContext(ctx)
	if r == nil || clientSpan == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suppressed || r.sealed || len(r.stepStack) == 0 {
		return
	}
	top := &r.stepStack[len(r.stepStack)-1]
	top.spanID = clientSpan
	top.downstream = &eventv2.Downstream{
		Service:  downstreamService,
		Endpoint: endpoint,
		Kind:     "rpc",
	}
}

func cloneDeep(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = cloneDeep(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = cloneDeep(val)
		}
		return out
	}
	return v
}

type request struct {
	sdk *sdk

	eventID      string
	tsStart      time.Time
	traceID      string
	spanID       string
	parentSpanID string

	maxSteps int
	maxLogs  int
	maxBytes int

	mu sync.Mutex

	stepStack []activeStep
	steps     []stepBuf
	logs      []logBuf
	fields    F
	errs      []errEntry

	bufBytes int

	suppressed  bool
	sealed      bool
	headerOnly  bool // degraded by MaxBufferBytes pressure (§4.4)
	finalStatus eventv2.Status

	// First observable failing step, or "request" sentinel for a Fail() with
	// no active step. Set once and never overwritten.
	anchorStep          string
	anchorCode          string
	anchorFromStepPanic bool
	panicStepHint       string
}

type activeStep struct {
	name       string
	spanID     string
	downstream *eventv2.Downstream
	startedAt  time.Time
	startMS    int64
}

type stepBuf struct {
	name       string
	spanID     string
	startMS    int64
	durationMS int64
	status     string
	downstream *eventv2.Downstream
	err        *Error
}

type logBuf struct {
	tsOffsetMS int64
	level      string
	msg        string
	fields     F
	stepName   string // captured at log-time; used by Explain only
}

type errEntry struct {
	code   string
	reason string
}

func (r *request) addStepLocked(s stepBuf) {
	// Anchor and errors[] are tiny and survive buffer pressure — record
	// before any drop / degrade decision.
	if s.status == stepStatusError && s.err != nil {
		r.recordErrorLocked(s.err.Code, s.err.Reason)
		if r.anchorStep == "" {
			r.anchorStep = s.name
			r.anchorCode = s.err.Code
			r.anchorFromStepPanic = r.panicStepHint == s.name && s.err.Code == "ERR"
		}
	}

	if r.headerOnly {
		r.sdk.stepsDropped.Add(1)
		return
	}

	bytes := stepBytes(s)
	if r.bufBytes+bytes > r.maxBytes {
		r.dropOkLogsLocked()
		if r.bufBytes+bytes > r.maxBytes {
			r.dropOkStepsLocked()
		}
		if r.bufBytes+bytes > r.maxBytes {
			if s.status == stepStatusOK {
				r.sdk.stepsDropped.Add(1)
				r.sdk.bytesDropped.Add(int64(bytes))
				return
			}
			// Error step still won't fit → header-only fallback.
			r.degradeToHeaderOnlyLocked()
			r.sdk.stepsDropped.Add(1)
			return
		}
	}

	if len(r.steps) >= r.maxSteps {
		if s.status == stepStatusOK {
			r.sdk.stepsDropped.Add(1)
			return
		}
		if !r.evictOldestOkStepLocked() {
			r.sdk.stepsDropped.Add(1)
			return
		}
	}

	r.steps = append(r.steps, s)
	r.bufBytes += bytes
}

func (r *request) addLogLocked(l logBuf) {
	if r.headerOnly {
		r.sdk.logsDropped.Add(1)
		return
	}

	bytes := logBytes(l)
	if r.bufBytes+bytes > r.maxBytes {
		r.dropOkLogsLocked()
		if r.bufBytes+bytes > r.maxBytes {
			if l.level == "info" {
				r.sdk.logsDropped.Add(1)
				r.sdk.bytesDropped.Add(int64(bytes))
				return
			}
			// warn/error log still won't fit → header-only fallback.
			r.degradeToHeaderOnlyLocked()
			r.sdk.logsDropped.Add(1)
			return
		}
	}

	if len(r.logs) >= r.maxLogs {
		if l.level == "info" {
			r.sdk.logsDropped.Add(1)
			return
		}
		if !r.evictOldestInfoLogLocked() {
			r.sdk.logsDropped.Add(1)
			return
		}
	}

	r.logs = append(r.logs, l)
	r.bufBytes += bytes
}

func (r *request) activeStepLocked() string {
	if n := len(r.stepStack); n > 0 {
		return r.stepStack[n-1].name
	}
	return "request"
}

func (r *request) setHTTPStatusLocked(status int) {
	httpMap, _ := r.fields["http"].(map[string]any)
	if httpMap == nil {
		httpMap = map[string]any{}
		r.fields["http"] = httpMap
	}
	httpMap["status"] = status
}

func (r *request) markLifecycleLocked(status eventv2.Status, code string) {
	if r.suppressed {
		return
	}
	r.finalStatus = status
	r.anchorStep = r.activeStepLocked()
	if code == eventv2.CodePanic && r.anchorStep == "request" && r.panicStepHint != "" {
		r.anchorStep = r.panicStepHint
		r.panicStepHint = ""
	}
	r.anchorCode = code
	r.anchorFromStepPanic = false
}

// degradeToHeaderOnlyLocked discards buffered detail and switches the request
// to header-only emission. Anchor + fields + errs (already recorded) survive.
func (r *request) degradeToHeaderOnlyLocked() {
	if r.headerOnly {
		return
	}
	r.headerOnly = true
	r.sdk.bufferOverflows.Add(1)
	for _, l := range r.logs {
		r.sdk.bytesDropped.Add(int64(logBytes(l)))
	}
	for _, s := range r.steps {
		r.sdk.bytesDropped.Add(int64(stepBytes(s)))
	}
	r.steps = nil
	r.logs = nil
	r.bufBytes = 0
}

func (r *request) recordErrorLocked(code, reason string) {
	if code == "" {
		return
	}
	for _, e := range r.errs {
		if e.code == code {
			return
		}
	}
	r.errs = append(r.errs, errEntry{code: code, reason: reason})
}

func (r *request) dropOkLogsLocked() {
	if len(r.logs) == 0 {
		return
	}
	kept := r.logs[:0]
	for _, l := range r.logs {
		if l.level == "info" {
			b := logBytes(l)
			r.bufBytes -= b
			r.sdk.logsDropped.Add(1)
			r.sdk.bytesDropped.Add(int64(b))
			continue
		}
		kept = append(kept, l)
	}
	r.logs = kept
}

func (r *request) dropOkStepsLocked() {
	if len(r.steps) == 0 {
		return
	}
	kept := r.steps[:0]
	for _, s := range r.steps {
		if s.status == stepStatusOK && s.name != r.anchorStep {
			b := stepBytes(s)
			r.bufBytes -= b
			r.sdk.stepsDropped.Add(1)
			r.sdk.bytesDropped.Add(int64(b))
			continue
		}
		kept = append(kept, s)
	}
	r.steps = kept
}

func (r *request) evictOldestOkStepLocked() bool {
	for i, s := range r.steps {
		if s.status == stepStatusOK && s.name != r.anchorStep {
			r.bufBytes -= stepBytes(s)
			r.steps = append(r.steps[:i], r.steps[i+1:]...)
			r.sdk.stepsDropped.Add(1)
			return true
		}
	}
	return false
}

func (r *request) evictOldestInfoLogLocked() bool {
	for i, l := range r.logs {
		if l.level == "info" {
			r.bufBytes -= logBytes(l)
			r.logs = append(r.logs[:i], r.logs[i+1:]...)
			r.sdk.logsDropped.Add(1)
			return true
		}
	}
	return false
}

func newEventID() string { return newUUIDv4() }
func newTraceID() string { return randomHex(16) }
func newSpanID() string  { return randomHex(8) }

// newUUIDv4 formats a v4 UUID without intermediate string allocations.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		seedFromTime(b[:])
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}

func randomHex(n int) string {
	var buf [16]byte
	if _, err := rand.Read(buf[:n]); err != nil {
		seedFromTime(buf[:n])
	}
	return hex.EncodeToString(buf[:n])
}

func seedFromTime(b []byte) {
	nano := time.Now().UnixNano()
	for i := range b {
		b[i] = byte(nano >> (i % 8 * 8))
	}
}

// Conservative byte-size estimates for cap accounting; not exact JSON size.
// A stable upper bound is enough for the buffer-pressure cascade.
func stepBytes(s stepBuf) int {
	n := 32 + len(s.name) + len(s.spanID)
	if s.downstream != nil {
		n += len(s.downstream.Service) + len(s.downstream.Endpoint) + len(s.downstream.Kind) + 24
	}
	if s.err != nil {
		n += 16 + len(s.err.Code) + len(s.err.Reason) + len(s.err.Cause)
	}
	return n
}

func logBytes(l logBuf) int {
	n := 24 + len(l.level) + len(l.msg)
	for k, v := range l.fields {
		n += len(k) + 8
		if str, ok := v.(string); ok {
			n += len(str)
		}
	}
	return n
}
