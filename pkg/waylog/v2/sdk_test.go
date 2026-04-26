package waylogv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const schemaPath = "../../../docs/schema/v2.0.json"

type harness struct {
	t      *testing.T
	buf    *bytes.Buffer
	devBuf *bytes.Buffer
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	resetForTest()
	buf := &bytes.Buffer{}
	if cfg.Service == "" {
		cfg.Service = "checkout"
	}
	if cfg.Env == "" {
		cfg.Env = "test"
	}
	cfg.Output = buf
	if err := Init(cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return &harness{t: t, buf: buf, devBuf: &bytes.Buffer{}}
}

func (h *harness) lastEvent() *eventv2.Event {
	h.t.Helper()
	out := h.buf.Bytes()
	if len(out) == 0 {
		return nil
	}
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte{'\n'})
	last := lines[len(lines)-1]
	var ev eventv2.Event
	if err := json.Unmarshal(last, &ev); err != nil {
		h.t.Fatalf("unmarshal emitted event: %v\nraw=%s", err, last)
	}
	return &ev
}

func (h *harness) validateLast() {
	h.t.Helper()
	ev := h.lastEvent()
	if ev == nil {
		h.t.Fatal("no event emitted")
	}
	if err := eventv2.Validate(schemaPath, ev); err != nil {
		raw, _ := json.MarshalIndent(ev, "", "  ")
		h.t.Fatalf("emitted event fails v2.0 schema: %v\n%s", err, raw)
	}
}

func (h *harness) captureDevOutput() {
	h.t.Helper()
	s := getState()
	if s == nil {
		h.t.Fatal("sdk not initialized")
	}
	s.devOut = h.devBuf
}

func TestInitRequiresServiceAndEnv(t *testing.T) {
	resetForTest()
	if err := Init(Config{}); err == nil {
		t.Fatal("expected error when Service is empty")
	}
	resetForTest()
	if err := Init(Config{Service: "x"}); err == nil {
		t.Fatal("expected error when Env is empty")
	}
}

func TestInitRefusesReinitWithActiveRequests(t *testing.T) {
	newHarness(t, Config{})
	_ = Begin(context.Background(), BeginOptions{})
	// Don't reset; intentionally leave one request active.
	err := Init(Config{Service: "x", Env: "test"})
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestInitAllowedAfterDrain(t *testing.T) {
	newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	if _, err := Finalize(ctx); err != nil {
		t.Fatal(err)
	}
	// Active count back to zero — re-init must succeed.
	if err := Init(Config{Service: "y", Env: "test", Output: &bytes.Buffer{}}); err != nil {
		t.Fatalf("re-init after drain: %v", err)
	}
}

func TestOKRequestEmitsValidEvent(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	SetField(ctx, "http", map[string]any{"method": "GET", "route": "/health", "status": 200})
	From(ctx).Info("served", F{"latency_ms": 4})

	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("status=%s want ok", ev.Status)
	}
	if ev.Anchor != nil {
		t.Fatalf("ok event must not carry anchor: %+v", ev.Anchor)
	}
	if len(ev.Logs) != 1 || ev.Logs[0].Msg != "served" {
		t.Fatalf("log not buffered: %+v", ev.Logs)
	}
	if ev.Fields["http"] == nil {
		t.Fatalf("fields.http missing: %+v", ev.Fields)
	}
	if got := Stats().EventsEmitted; got != 1 {
		t.Fatalf("EventsEmitted=%d want 1", got)
	}
}

func TestStepFailureSynthesizesAnchor(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	werr := NewError("PMT_502", WithReason("upstream gateway 5xx"))
	_ = StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		return werr
	})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "payment.charge" || ev.Anchor.ErrorCode != "PMT_502" {
		t.Fatalf("anchor wrong: %+v", ev.Anchor)
	}
	if len(ev.Steps) != 1 || ev.Steps[0].Status != "error" || ev.Steps[0].Error == nil ||
		ev.Steps[0].Error.Code != "PMT_502" {
		t.Fatalf("step failure not recorded: %+v", ev.Steps)
	}
	if len(ev.Errors) != 1 || ev.Errors[0].Code != "PMT_502" {
		t.Fatalf("errors[] not deduped/recorded: %+v", ev.Errors)
	}
}

func TestExplicitFailWithNoActiveStepUsesRequestSentinel(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	Fail(ctx, NewError("AUTH_DENIED"))
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Anchor == nil || ev.Anchor.Step != "request" || ev.Anchor.ErrorCode != "AUTH_DENIED" {
		t.Fatalf("anchor wrong on bare Fail: %+v", ev.Anchor)
	}
}

func TestFirstFailingStepWinsAnchor(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	_ = StepVoid(ctx, "first", func(ctx context.Context) error {
		return NewError("FIRST_ERR")
	})
	_ = StepVoid(ctx, "second", func(ctx context.Context) error {
		return NewError("SECOND_ERR")
	})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Anchor.Step != "first" || ev.Anchor.ErrorCode != "FIRST_ERR" {
		t.Fatalf("first failure must own anchor: %+v", ev.Anchor)
	}
	if len(ev.Errors) != 2 {
		t.Fatalf("errors[] should contain both codes deduped: %+v", ev.Errors)
	}
}

func TestSuppressEmitsHeaderOnlyEvent(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	From(ctx).Info("first log")
	Suppress(ctx)
	From(ctx).Warn("after suppress")
	SetField(ctx, "user", map[string]any{"id": "u_1"})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusSuppressed {
		t.Fatalf("status=%s want suppressed", ev.Status)
	}
	if len(ev.Steps) != 0 || len(ev.Logs) != 0 {
		t.Fatalf("suppressed event must have empty steps/logs: %+v / %+v", ev.Steps, ev.Logs)
	}
	if ev.Anchor != nil {
		t.Fatalf("suppressed event must not carry anchor: %+v", ev.Anchor)
	}
	stats := Stats()
	if stats.EventsEmitted != 1 || stats.EventsSuppressed != 1 {
		t.Fatalf("suppressed must increment both EventsEmitted and EventsSuppressed: %+v", stats)
	}
}

func TestSuppressThenFailKeepsSuppressedAndCounts(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	Suppress(ctx)
	Fail(ctx, NewError("LATE_FAIL"))
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusSuppressed {
		t.Fatalf("status=%s want suppressed", ev.Status)
	}
	if got := Stats().SuppressedThenFailed; got != 1 {
		t.Fatalf("SuppressedThenFailed=%d want 1", got)
	}
}

func TestFinalizePanicSynthesizesReservedLifecycleCode(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	r := requestFromContext(ctx)
	now := time.Now()
	r.pushStep("payment.charge", now, int64(now.Sub(r.tsStart)/1e6))

	_, _ = FinalizePanic(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "payment.charge" || ev.Anchor.ErrorCode != eventv2.CodePanic {
		t.Fatalf("panic anchor wrong: %+v", ev.Anchor)
	}
}

func TestFinalizeAbortedPreservesExistingExplicitFail(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	Fail(ctx, NewError("AUTH_DENIED"))

	_, _ = FinalizeAborted(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "request" || ev.Anchor.ErrorCode != "AUTH_DENIED" {
		t.Fatalf("explicit failure must survive aborted finalize: %+v", ev.Anchor)
	}
}

func TestSetHTTPStatusUpdatesFieldsMap(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	SetField(ctx, "http", map[string]any{"method": "GET", "route": "/health", "status": 200})
	SetHTTPStatus(ctx, 502)

	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	httpMap, _ := ev.Fields["http"].(map[string]any)
	if got, _ := httpMap["status"].(float64); got != 502 {
		t.Fatalf("fields.http.status=%v want 502", httpMap["status"])
	}
}

func TestNewErrorRejectsReservedCodes(t *testing.T) {
	newHarness(t, Config{})
	for _, code := range []string{eventv2.CodeTimeout, eventv2.CodeAborted, eventv2.CodePanic, eventv2.CodePartial} {
		got := NewError(code)
		if got != nil {
			t.Fatalf("NewError(%q) must return nil; got %+v", code, got)
		}
	}
	if got := Stats().ReservedCodeRejections; got != 4 {
		t.Fatalf("ReservedCodeRejections=%d want 4", got)
	}
}

func TestFailRejectsHandCraftedReservedCode(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	// Bypass NewError by hand-crafting *Error.
	Fail(ctx, &Error{Code: eventv2.CodeTimeout, Reason: "tried to spoof"})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("hand-crafted reserved code must not become anchor; status=%s", ev.Status)
	}
	if got := Stats().ReservedCodeRejections; got != 1 {
		t.Fatalf("ReservedCodeRejections=%d want 1", got)
	}
}

func TestStepReturningReservedCodeIsRewrittenToERR(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	_ = StepVoid(ctx, "shady", func(ctx context.Context) error {
		return &Error{Code: eventv2.CodePanic, Reason: "user spoof"}
	})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Anchor == nil || ev.Anchor.ErrorCode != "ERR" {
		t.Fatalf("reserved code from user step must collapse to ERR: %+v", ev.Anchor)
	}
	if got := Stats().ReservedCodeRejections; got != 1 {
		t.Fatalf("ReservedCodeRejections=%d want 1 (Step path must count rejection)", got)
	}
}

func TestNoActiveRequestNoOps(t *testing.T) {
	resetForTest()
	if err := Init(Config{Service: "x", Env: "test"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	From(ctx).Info("nope")
	From(ctx).Error("nope", NewError("X"))
	Suppress(ctx)
	Fail(ctx, NewError("Y"))
	if _, err := Explain(ctx); !errors.Is(err, ErrNoActiveRequest) {
		t.Fatalf("Explain on bare ctx must return ErrNoActiveRequest, got %v", err)
	}
}

func TestStepPropagatesNonWaylogError(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	plain := errors.New("disk full")
	_ = StepVoid(ctx, "db.write", func(ctx context.Context) error { return plain })
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Anchor == nil || ev.Anchor.Step != "db.write" || ev.Anchor.ErrorCode != "ERR" {
		t.Fatalf("plain error must produce ERR-coded anchor: %+v", ev.Anchor)
	}
	if ev.Steps[0].Error.Reason != "disk full" {
		t.Fatalf("plain error reason not preserved: %+v", ev.Steps[0].Error)
	}
}

func TestMaxLogsDropsInfoFirst(t *testing.T) {
	h := newHarness(t, Config{MaxLogs: 4})
	ctx := Begin(context.Background(), BeginOptions{})

	From(ctx).Info("i1")
	From(ctx).Info("i2")
	From(ctx).Warn("w1")
	From(ctx).Info("i3")
	From(ctx).Error("e1", NewError("X"))
	From(ctx).Info("i4")
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if len(ev.Logs) != 4 {
		t.Fatalf("want 4 logs after cap, got %d: %+v", len(ev.Logs), ev.Logs)
	}
	for _, l := range ev.Logs {
		if l.Msg == "i4" {
			t.Fatalf("dropped info should not be present: %+v", ev.Logs)
		}
	}
	hasWarn, hasError := false, false
	for _, l := range ev.Logs {
		if l.Level == "warn" {
			hasWarn = true
		}
		if l.Level == "error" {
			hasError = true
		}
	}
	if !hasWarn || !hasError {
		t.Fatalf("warn/error logs must be preserved under cap: %+v", ev.Logs)
	}
	if got := Stats().LogsDropped; got < 1 {
		t.Fatalf("LogsDropped counter not incremented: %d", got)
	}
}

func TestMaxStepsDropsOkFirst(t *testing.T) {
	h := newHarness(t, Config{MaxSteps: 3})
	ctx := Begin(context.Background(), BeginOptions{})

	_ = StepVoid(ctx, "ok1", func(ctx context.Context) error { return nil })
	_ = StepVoid(ctx, "ok2", func(ctx context.Context) error { return nil })
	_ = StepVoid(ctx, "ok3", func(ctx context.Context) error { return nil })
	_ = StepVoid(ctx, "boom", func(ctx context.Context) error { return NewError("BOOM") })
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if len(ev.Steps) != 3 {
		t.Fatalf("want 3 steps after cap, got %d: %+v", len(ev.Steps), ev.Steps)
	}
	hasBoom := false
	for _, s := range ev.Steps {
		if s.Name == "boom" {
			hasBoom = true
		}
	}
	if !hasBoom {
		t.Fatalf("error step must be present after eviction: %+v", ev.Steps)
	}
}

func TestMaxStepsDropsIncomingOkWhenFull(t *testing.T) {
	h := newHarness(t, Config{MaxSteps: 2})
	ctx := Begin(context.Background(), BeginOptions{})

	_ = StepVoid(ctx, "boom", func(ctx context.Context) error { return NewError("BOOM") })
	_ = StepVoid(ctx, "ok1", func(ctx context.Context) error { return nil })
	_ = StepVoid(ctx, "ok2", func(ctx context.Context) error { return nil })
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if len(ev.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d: %+v", len(ev.Steps), ev.Steps)
	}
	for _, s := range ev.Steps {
		if s.Name == "ok2" {
			t.Fatalf("incoming ok step should be dropped, not buffered: %+v", ev.Steps)
		}
	}
	if got := Stats().StepsDropped; got < 1 {
		t.Fatalf("StepsDropped not incremented: %d", got)
	}
}

func TestMaxBufferBytesDegradesToHeaderOnly(t *testing.T) {
	h := newHarness(t, Config{MaxBufferBytes: 256, MaxLogs: 1024, MaxSteps: 1024})
	ctx := Begin(context.Background(), BeginOptions{})

	bigPayload := strings.Repeat("x", 512)
	From(ctx).Warn("big-warn-1", F{"blob": bigPayload})
	From(ctx).Warn("big-warn-2", F{"blob": bigPayload})

	Fail(ctx, NewError("BUDGET_BLOWN"))
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	if len(ev.Logs) != 0 || len(ev.Steps) != 0 {
		t.Fatalf("header-heavy fallback must drop steps/logs: steps=%d logs=%d", len(ev.Steps), len(ev.Logs))
	}
	if ev.Anchor == nil || ev.Anchor.ErrorCode != "BUDGET_BLOWN" {
		t.Fatalf("anchor must survive header-only fallback: %+v", ev.Anchor)
	}
	if got := Stats().BufferOverflows; got != 1 {
		t.Fatalf("BufferOverflows=%d want 1", got)
	}
}

func TestRedactorMutatesEventFields(t *testing.T) {
	h := newHarness(t, Config{
		Redactor: func(f F) F {
			if u, ok := f["user"].(map[string]any); ok {
				u["id"] = "REDACTED"
				f["user"] = u
			}
			return f
		},
	})
	ctx := Begin(context.Background(), BeginOptions{})
	SetField(ctx, "user", map[string]any{"id": "u_secret"})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	user, _ := ev.Fields["user"].(map[string]any)
	if user["id"] != "REDACTED" {
		t.Fatalf("redactor not applied: %+v", ev.Fields)
	}
}

func TestLoggerFieldsImmuneToCallerMutation(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	fields := F{"latency_ms": 4, "route": "/x"}
	From(ctx).Info("served", fields)
	fields["latency_ms"] = 99999

	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if len(ev.Logs) != 1 {
		t.Fatalf("want 1 log, got %d", len(ev.Logs))
	}
	got := ev.Logs[0].Fields
	// JSON unmarshal returns float64 for numbers.
	if v, _ := got["latency_ms"].(float64); v != 4 {
		t.Fatalf("caller mutation leaked into log fields: latency_ms=%v want 4", got["latency_ms"])
	}
}

func TestSetFieldDeepClonesNestedMapsAndSlices(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	payload := map[string]any{
		"route":  "/x",
		"nested": map[string]any{"deep": "v1"},
		"list":   []any{map[string]any{"k": "v1"}},
	}
	SetField(ctx, "http", payload)

	payload["route"] = "/y"
	payload["nested"].(map[string]any)["deep"] = "MUTATED"
	payload["list"].([]any)[0].(map[string]any)["k"] = "MUTATED"

	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	got, _ := ev.Fields["http"].(map[string]any)
	if got["route"] != "/x" {
		t.Fatalf("top-level mutation leaked: route=%v want /x", got["route"])
	}
	if nested, _ := got["nested"].(map[string]any); nested["deep"] != "v1" {
		t.Fatalf("nested-map mutation leaked: deep=%v want v1", nested["deep"])
	}
	list, _ := got["list"].([]any)
	if first, _ := list[0].(map[string]any); first["k"] != "v1" {
		t.Fatalf("nested-slice element mutation leaked: k=%v want v1", first["k"])
	}
}

func TestLoggerFieldsHonorShallowNestedContract(t *testing.T) {
	// Pins the documented shallow contract for the per-call hot path:
	// mergeFields copies the outer F but does NOT walk nested maps/slices.
	// Snapshot-at-call lives in SetField; logger callers must not mutate
	// nested objects after passing them. If this contract changes (e.g.
	// promoted to deep clone), update mergeFields' doc and flip this test.
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	nested := map[string]any{"route": "/x"}
	From(ctx).Info("served", F{"http": nested})
	nested["route"] = "/y"

	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	got, _ := ev.Logs[0].Fields["http"].(map[string]any)
	if got["route"] != "/y" {
		t.Fatalf("logger nested contract regressed to deep clone: route=%v want /y", got["route"])
	}
}

func TestExplainSnapshotMidRequest(t *testing.T) {
	newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	SetField(ctx, "http", map[string]any{"route": "/checkout"})
	From(ctx).Info("step1")
	_ = StepVoid(ctx, "db.load_cart", func(ctx context.Context) error { return nil })

	res, err := Explain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Route != "/checkout" || res.Status != "ok" {
		t.Fatalf("unexpected snapshot: %+v", res)
	}
	if len(res.Path) != 1 || res.Path[0].Name != "db.load_cart" {
		t.Fatalf("path missing closed step: %+v", res.Path)
	}
	if !strings.Contains(res.String(), "db.load_cart") {
		t.Fatalf("String() should include step name: %s", res.String())
	}

	_, _ = Finalize(ctx)
}

func TestExplainIncludesDownstreamEdges(t *testing.T) {
	newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	_ = StepVoid(ctx, "payment.charge", func(ctx context.Context) error {
		RecordOutgoingSpan(ctx, "9d7a1b3e2c4d5e6f", "payment", "POST /charge")
		return nil
	})

	res, err := Explain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Downstream) != 1 {
		t.Fatalf("want 1 downstream edge, got %+v", res.Downstream)
	}
	edge := res.Downstream[0]
	if edge.Step != "payment.charge" || edge.Service != "payment" || edge.Endpoint != "POST /charge" {
		t.Fatalf("downstream edge wrong: %+v", edge)
	}
	if !strings.Contains(res.String(), "payment.charge -> payment (POST /charge)") {
		t.Fatalf("String() should include downstream edge: %s", res.String())
	}
}

func TestConcurrentLoggingDoesNotRace(t *testing.T) {
	newHarness(t, Config{MaxLogs: 100})
	ctx := Begin(context.Background(), BeginOptions{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				From(ctx).Info("hello", F{"n": n, "j": j})
			}
		}(i)
	}
	wg.Wait()

	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func TestFinalizeTwiceCountsLateCompletion(t *testing.T) {
	newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	if _, err := Finalize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("second Finalize must be a no-op, got %v", err)
	}
	if got := Stats().LateCompletionAfterEmit; got != 1 {
		t.Fatalf("LateCompletionAfterEmit=%d want 1", got)
	}
}

func TestShutdownReturnsAfterAllRequestsFinalize(t *testing.T) {
	newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = Finalize(ctx)
	}()

	deadline, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := Shutdown(deadline); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := Stats().ActiveRequests; got != 0 {
		t.Fatalf("ActiveRequests=%d want 0", got)
	}
}

func TestEmptyStepNameRunsFnButOpensNoStep(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})

	called := false
	_ = StepVoid(ctx, "", func(ctx context.Context) error {
		called = true
		return nil
	})
	_, _ = Finalize(ctx)

	if !called {
		t.Fatal("Step with empty name must still call fn")
	}
	h.validateLast()
	ev := h.lastEvent()
	if len(ev.Steps) != 0 {
		t.Fatalf("empty-name step must not appear in steps[]: %+v", ev.Steps)
	}
}

func TestFailWithEmptyCodeIsNoOp(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := Begin(context.Background(), BeginOptions{})
	Fail(ctx, &Error{Reason: "no code"})
	_, _ = Finalize(ctx)

	h.validateLast()
	ev := h.lastEvent()
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("Fail with empty code must be a no-op; got status=%s", ev.Status)
	}
}

func TestShutdownTimesOutWithStuckRequest(t *testing.T) {
	newHarness(t, Config{})
	_ = Begin(context.Background(), BeginOptions{})
	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Shutdown(deadline); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestIngestURLRoutesFinalEventToTransport(t *testing.T) {
	var (
		mu  sync.Mutex
		got *eventv2.Event
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev eventv2.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Fatalf("decode ingest payload: %v", err)
		}
		mu.Lock()
		got = &ev
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t, Config{IngestURL: srv.URL})
	ctx := Begin(context.Background(), BeginOptions{})
	SetField(ctx, "http", map[string]any{"method": "GET", "route": "/health", "status": 200})
	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(deadline); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if h.buf.Len() != 0 {
		t.Fatalf("local writer should stay empty when IngestURL is set, got %q", h.buf.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.TraceID == "" {
		t.Fatalf("transport did not receive emitted event: %+v", got)
	}
}

func TestInvalidIngestURLRejectsInit(t *testing.T) {
	resetForTest()
	if err := Init(Config{Service: "checkout", Env: "test", IngestURL: "localhost:8080"}); err == nil {
		t.Fatal("expected invalid IngestURL error")
	}
	if getState() != nil {
		t.Fatal("invalid Init must not install SDK state")
	}
}

func TestMaxInFlightBytesDropsOversizedTransportEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("oversized event should be rejected before transport POST")
	}))
	defer srv.Close()

	newHarness(t, Config{IngestURL: srv.URL, MaxInFlightBytes: 32})
	ctx := Begin(context.Background(), BeginOptions{})
	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if got := Stats().EventsDropped; got != 1 {
		t.Fatalf("EventsDropped=%d want 1", got)
	}
}

func TestMaxEventsPerSecDropsOKBeforePriority(t *testing.T) {
	h := newHarness(t, Config{MaxEventsPerSec: 1})

	ok1 := Begin(context.Background(), BeginOptions{})
	_, _ = Finalize(ok1)

	ok2 := Begin(context.Background(), BeginOptions{})
	_, _ = Finalize(ok2)

	boom := Begin(context.Background(), BeginOptions{})
	Fail(boom, NewError("BOOM"))
	_, _ = Finalize(boom)

	lines := bytes.Split(bytes.TrimSpace(h.buf.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("emitted lines=%d want 2; output=%s", len(lines), h.buf.String())
	}
	if got := Stats().EventsDropped; got != 1 {
		t.Fatalf("EventsDropped=%d want 1", got)
	}
}

func TestDevModeEmitsPrettyLogsAndFinalEvent(t *testing.T) {
	h := newHarness(t, Config{DevMode: true})
	h.captureDevOutput()
	ctx := Begin(context.Background(), BeginOptions{})

	From(ctx).Info("served", F{"route": "/x"})
	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	out := h.devBuf.String()
	if !strings.Contains(out, "[INFO] checkout") || !strings.Contains(out, "served route=/x") {
		t.Fatalf("pretty log line missing: %q", out)
	}
	if !strings.Contains(out, "\"status\": \"ok\"") || !strings.Contains(out, "\"service\": \"checkout\"") {
		t.Fatalf("pretty final JSON missing: %q", out)
	}
}

func TestNonDevModeSuppressesPrettyOutput(t *testing.T) {
	h := newHarness(t, Config{})
	h.captureDevOutput()
	ctx := Begin(context.Background(), BeginOptions{})

	From(ctx).Info("served")
	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if h.devBuf.Len() != 0 {
		t.Fatalf("non-dev mode must not emit pretty output: %q", h.devBuf.String())
	}
}

func TestDevModeConcurrentLoggingRemainsRaceSafe(t *testing.T) {
	h := newHarness(t, Config{DevMode: true, MaxLogs: 100})
	h.captureDevOutput()
	ctx := Begin(context.Background(), BeginOptions{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				From(ctx).Info("hello", F{"n": n, "j": j})
			}
		}(i)
	}
	wg.Wait()

	if _, err := Finalize(ctx); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if !strings.Contains(h.devBuf.String(), "\"status\": \"ok\"") {
		t.Fatalf("expected final pretty JSON in dev output: %q", h.devBuf.String())
	}
}
