package ingestv2

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sssmaran/WaylogCLI/internal/auth"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestEventsSingleJSONValid(t *testing.T) {
	h, wal := newTestHandler(t, nil)
	rec := post(t, h, "application/json", "", validEventJSON("00000000-0000-4000-8000-000000000001"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env IngestEnvelope
	decodeEnvelope(t, rec, &env)
	if env.Accepted != 1 || env.Duplicate != 0 || len(env.Rejected) != 0 || len(env.Deprecations) != 0 {
		t.Fatalf("env=%+v", env)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["rejected"]) != "[]" {
		t.Fatalf("rejected wire=%s want []", wire["rejected"])
	}
	if string(wire["deprecations"]) != "{}" {
		t.Fatalf("deprecations wire=%s want {}", wire["deprecations"])
	}
	if got := wal.Count(); got != 1 {
		t.Fatalf("wal writes=%d want 1", got)
	}
}

func TestEventsRejectsV1BridgeNotImplemented(t *testing.T) {
	raw := validEventMap("00000000-0000-4000-8000-000000000001")
	raw["schema_version"] = "1.1"

	h, _ := newTestHandler(t, nil)
	rec := post(t, h, "application/json", "", mustJSON(t, raw))
	env := expectEnvelopeStatus(t, rec, http.StatusOK)
	if env.Accepted != 0 || len(env.Rejected) != 1 || env.Rejected[0].Reason != ReasonBridgeNotImplemented {
		t.Fatalf("env=%+v", env)
	}
}

func TestEventsUnsupportedContentEncoding(t *testing.T) {
	for _, encoding := range []string{"br", "deflate", "compress"} {
		t.Run(encoding, func(t *testing.T) {
			h, _ := newTestHandler(t, nil)
			rec := post(t, h, "application/json", encoding, validEventJSON("00000000-0000-4000-8000-000000000001"))
			env := expectEnvelopeStatus(t, rec, http.StatusBadRequest)
			if env.Rejected[0].Reason != ReasonUnsupportedEncoding {
				t.Fatalf("reason=%q", env.Rejected[0].Reason)
			}
		})
	}
}

func TestEventsBodySizeBoundaries(t *testing.T) {
	exact := exactSizedJSON(t, maxBodyBytes)
	h, _ := newTestHandler(t, nil)
	rec := post(t, h, "application/json", "", exact)
	env := expectEnvelopeStatus(t, rec, http.StatusOK)
	if env.Accepted != 1 {
		t.Fatalf("accepted=%d body=%s", env.Accepted, rec.Body.String())
	}

	tooLarge := exactSizedJSON(t, maxBodyBytes+1)
	h, _ = newTestHandler(t, nil)
	rec = post(t, h, "application/json", "", tooLarge)
	env = expectEnvelopeStatus(t, rec, http.StatusRequestEntityTooLarge)
	if env.Rejected[0].Reason != ReasonBodyOversize {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}

	h, _ = newTestHandler(t, nil)
	rec = postBytes(t, h, "application/json", "gzip", gzipBytes([]byte(tooLarge)))
	env = expectEnvelopeStatus(t, rec, http.StatusRequestEntityTooLarge)
	if env.Rejected[0].Reason != ReasonBodyOversize {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsSchemaValidationFailures(t *testing.T) {
	for _, field := range []string{"service", "ts_start", "ts_end"} {
		t.Run(field, func(t *testing.T) {
			raw := validEventMap("00000000-0000-4000-8000-000000000001")
			delete(raw, field)
			h, _ := newTestHandler(t, nil)
			rec := post(t, h, "application/json", "", mustJSON(t, raw))
			env := expectEnvelopeStatus(t, rec, http.StatusOK)
			if env.Accepted != 0 || len(env.Rejected) != 1 || env.Rejected[0].Reason != ReasonSchemaValidationFailed {
				t.Fatalf("env=%+v", env)
			}
			if env.Rejected[0].Detail == "" || !strings.Contains(env.Rejected[0].Detail, field) {
				t.Fatalf("detail=%q does not mention %s", env.Rejected[0].Detail, field)
			}
		})
	}
}

func TestEventsNDJSONBatches(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	body := strings.Join([]string{
		validEventJSON("00000000-0000-4000-8000-000000000001"),
		validEventJSON("00000000-0000-4000-8000-000000000002"),
		validEventJSON("00000000-0000-4000-8000-000000000003"),
	}, "\n") + "\n"
	env := expectEnvelopeStatus(t, post(t, h, "application/x-ndjson", "", body), http.StatusOK)
	if env.Accepted != 3 || len(env.Rejected) != 0 {
		t.Fatalf("env=%+v", env)
	}

	mixed := strings.Join([]string{
		validEventJSON("00000000-0000-4000-8000-000000000004"),
		"{bad",
		validEventJSON("00000000-0000-4000-8000-000000000005"),
	}, "\n")
	env = expectEnvelopeStatus(t, post(t, h, "application/x-ndjson", "", mixed), http.StatusOK)
	if env.Accepted != 2 || len(env.Rejected) != 1 || env.Rejected[0].Index != 1 || env.Rejected[0].Reason != ReasonInvalidJSON {
		t.Fatalf("env=%+v", env)
	}

	env = expectEnvelopeStatus(t, postBytes(t, h, "application/x-ndjson", "gzip", gzipBytes([]byte(body))), http.StatusOK)
	if env.Accepted != 0 || env.Duplicate != 3 {
		t.Fatalf("env=%+v", env)
	}
}

func TestEventsBatchOversize(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxBatchItems+1; i++ {
		b.WriteString(validEventJSON(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)))
		b.WriteByte('\n')
	}
	h, _ := newTestHandler(t, nil)
	env := expectEnvelopeStatus(t, post(t, h, "application/x-ndjson", "", b.String()), http.StatusRequestEntityTooLarge)
	if env.Rejected[0].Reason != ReasonBatchOversize {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsGzipInvalidBody(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	rec := post(t, h, "application/x-ndjson", "gzip", "not gzip")
	env := expectEnvelopeStatus(t, rec, http.StatusBadRequest)
	if env.Rejected[0].Reason != ReasonInvalidBody {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsContentTypeHandling(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	expectEnvelopeStatus(t, post(t, h, "application/json; charset=utf-8", "", validEventJSON("00000000-0000-4000-8000-000000000001")), http.StatusOK)

	ndjson := validEventJSON("00000000-0000-4000-8000-000000000002") + "\n"
	expectEnvelopeStatus(t, post(t, h, "application/x-ndjson; charset=utf-8", "", ndjson), http.StatusOK)

	rec := post(t, h, "", "", validEventJSON("00000000-0000-4000-8000-000000000003"))
	env := expectEnvelopeStatus(t, rec, http.StatusBadRequest)
	if env.Rejected[0].Reason != ReasonInvalidBody {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}

	rec = post(t, h, "text/plain", "", validEventJSON("00000000-0000-4000-8000-000000000004"))
	env = expectEnvelopeStatus(t, rec, http.StatusUnsupportedMediaType)
	if env.Rejected[0].Reason != ReasonUnsupportedContentType {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsAuthAndMethod(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	protected := auth.Middleware("write", []string{"test"}, nil)(http.HandlerFunc(h.Events))

	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(validEventJSON("00000000-0000-4000-8000-000000000001")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec = httptest.NewRecorder()
	h.Events(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", rec.Code)
	}
}

func TestEventsConcurrentSchemaReuse(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	body := validEventJSON("00000000-0000-4000-8000-000000000001")
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(t, h, "application/json", "", body)
			if rec.Code != http.StatusOK {
				errs <- rec.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestEventsMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	h, _ := newTestHandler(t, m)

	offset := 0
	for _, n := range []int{1, 16, 256} {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(validEventJSON(fmt.Sprintf("00000000-0000-4000-8000-%012d", offset+i+1)))
			b.WriteByte('\n')
		}
		offset += n
		expectEnvelopeStatus(t, post(t, h, "application/x-ndjson", "", b.String()), http.StatusOK)
	}

	fm := gatherMap(t, reg)
	if got := histogramCount(fm["waylog_ingest_batch_size"]); got != 3 {
		t.Fatalf("batch histogram count=%v want 3", got)
	}
	if got := counterWithLabel(fm["waylog_events_rejected_total"], "reason", ReasonSchemaValidationFailed); got != 0 {
		t.Fatalf("schema_validation_failed preinit=%v want 0", got)
	}

	raw := validEventMap("00000000-0000-4000-8000-000000000001")
	delete(raw, "service")
	expectEnvelopeStatus(t, post(t, h, "application/json", "", mustJSON(t, raw)), http.StatusOK)
	fm = gatherMap(t, reg)
	if got := counterWithLabel(fm["waylog_events_rejected_total"], "reason", ReasonSchemaValidationFailed); got != 1 {
		t.Fatalf("schema_validation_failed=%v want 1", got)
	}
	if got := counterValue(fm["waylog_events_accepted_total"]); got != 273 {
		t.Fatalf("events_accepted=%v want 273", got)
	}
}

func TestEventsDedupeWritesOnlyOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	index := NewRecentIndex(nil)
	h, wal := newTestHandlerWithIndex(t, m, index)
	body := validEventJSON("00000000-0000-4000-8000-000000000001")

	env := expectEnvelopeStatus(t, post(t, h, "application/json", "", body), http.StatusOK)
	if env.Accepted != 1 || env.Duplicate != 0 {
		t.Fatalf("first env=%+v", env)
	}
	env = expectEnvelopeStatus(t, post(t, h, "application/json", "", body), http.StatusOK)
	if env.Accepted != 0 || env.Duplicate != 1 {
		t.Fatalf("second env=%+v", env)
	}
	if got := wal.Count(); got != 1 {
		t.Fatalf("wal writes=%d want 1", got)
	}
	if got := index.Sizes().Events; got != 1 {
		t.Fatalf("indexed events=%d want 1", got)
	}
	fm := gatherMap(t, reg)
	if got := counterValue(fm["waylog_events_duplicate_total"]); got != 1 {
		t.Fatalf("events_duplicate=%v want 1", got)
	}
}

func TestEventsAcceptedEventIsIndexed(t *testing.T) {
	index := NewRecentIndex(nil)
	h, _ := newTestHandlerWithIndex(t, nil, index)
	eventID := "00000000-0000-4000-8000-000000000001"

	env := expectEnvelopeStatus(t, post(t, h, "application/json", "", validEventJSON(eventID)), http.StatusOK)
	if env.Accepted != 1 {
		t.Fatalf("env=%+v", env)
	}
	if _, ok := index.GetByID(eventID); !ok {
		t.Fatal("accepted event not indexed")
	}
}

func TestEventsConcurrentDuplicateWritesOnce(t *testing.T) {
	wal := &fakeWAL{delay: 20 * time.Millisecond}
	h := newTestHandlerWithConfig(t, Config{
		Dedup: NewDedup(DefaultDedupCapacity, nil),
		WAL:   wal,
	})
	body := validEventJSON("00000000-0000-4000-8000-000000000001")

	var wg sync.WaitGroup
	envs := make(chan IngestEnvelope, 32)
	errs := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := post(t, h, "application/json", "", body)
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
			var env IngestEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				errs <- fmt.Sprintf("decode envelope: %v body=%s", err, rec.Body.String())
				return
			}
			envs <- env
		}()
	}
	wg.Wait()
	close(envs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	accepted, duplicate := 0, 0
	for env := range envs {
		accepted += env.Accepted
		duplicate += env.Duplicate
	}
	if accepted != 1 || duplicate != 31 {
		t.Fatalf("accepted=%d duplicate=%d want 1/31", accepted, duplicate)
	}
	if got := wal.Count(); got != 1 {
		t.Fatalf("wal writes=%d want 1", got)
	}
}

func TestEventsWALFailureReturnsPlain503(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	wal := &fakeWAL{failAt: 1}
	h := newTestHandlerWithWAL(t, m, wal)

	rec := post(t, h, "application/json", "", validEventJSON("00000000-0000-4000-8000-000000000001"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("503 should not return JSON content type: %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "durability unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
	fm := gatherMap(t, reg)
	if got := counterWithLabel(fm["waylog_events_rejected_total"], "reason", ReasonDurabilityUnavailable); got != 1 {
		t.Fatalf("durability_unavailable=%v want 1", got)
	}
	if got := counterValue(fm["waylog_events_accepted_total"]); got != 0 {
		t.Fatalf("events_accepted=%v want 0", got)
	}
}

func TestEventsWALFailureMidBatchLeavesPriorEventDurableAndDeduped(t *testing.T) {
	wal := &fakeWAL{failAt: 2}
	dedup := NewDedup(10, nil)
	index := NewRecentIndex(nil)
	h := newTestHandlerWithConfig(t, Config{Dedup: dedup, WAL: wal, Index: index})
	body := strings.Join([]string{
		validEventJSON("00000000-0000-4000-8000-000000000001"),
		validEventJSON("00000000-0000-4000-8000-000000000002"),
		validEventJSON("00000000-0000-4000-8000-000000000003"),
	}, "\n")

	rec := post(t, h, "application/x-ndjson", "", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if wal.Count() != 1 {
		t.Fatalf("wal writes=%d want 1", wal.Count())
	}
	if !dedup.Seen("00000000-0000-4000-8000-000000000001") {
		t.Fatal("first event should be deduped after successful WAL write")
	}
	if _, ok := index.GetByID("00000000-0000-4000-8000-000000000001"); !ok {
		t.Fatal("first event should be indexed after successful WAL write")
	}
}

func TestValidateDoesNotMutateWALOrDedup(t *testing.T) {
	dedup := NewDedup(10, nil)
	wal := &fakeWAL{}
	index := NewRecentIndex(nil)
	h := newTestHandlerWithConfig(t, Config{Dedup: dedup, WAL: wal, Index: index})
	raw := validEventMap("00000000-0000-4000-8000-000000000002")
	delete(raw, "service")
	body := validEventJSON("00000000-0000-4000-8000-000000000001") + "\n" + mustJSON(t, raw)

	rec := validatePost(t, h, "application/x-ndjson", "", body)
	env := expectEnvelopeStatus(t, rec, http.StatusOK)
	if env.Accepted != 1 || env.Duplicate != 0 || len(env.Rejected) != 1 {
		t.Fatalf("env=%+v", env)
	}
	if wal.Count() != 0 {
		t.Fatalf("wal writes=%d want 0", wal.Count())
	}
	if dedup.Size() != 0 {
		t.Fatalf("dedup size=%d want 0", dedup.Size())
	}
	if index.Sizes().Events != 0 {
		t.Fatalf("indexed events=%d want 0", index.Sizes().Events)
	}
}

func TestEventsProjectionPanicReturns503AndRollsBackDedup(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	dedup := NewDedup(10, nil)
	wal := &fakeWAL{}
	h := newTestHandlerWithConfig(t, Config{
		Metrics: m,
		Dedup:   dedup,
		WAL:     wal,
		Project: panicProjector{},
	})
	eventID := "00000000-0000-4000-8000-000000000001"

	rec := post(t, h, "application/json", "", validEventJSON(eventID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "projection unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if wal.Count() != 1 {
		t.Fatalf("wal writes=%d want 1", wal.Count())
	}
	if dedup.Seen(eventID) {
		t.Fatal("dedup entry should be rolled back")
	}
	fm := gatherMap(t, reg)
	if got := counterValue(fm["waylog_events_accepted_total"]); got != 0 {
		t.Fatalf("events_accepted=%v want 0", got)
	}
	if got := counterValue(fm["waylog_v2_project_panic_total"]); got != 1 {
		t.Fatalf("project_panic=%v want 1", got)
	}
}

func newTestHandler(t *testing.T, m *metrics.Metrics) (*Handler, *fakeWAL) {
	t.Helper()
	wal := &fakeWAL{}
	h := newTestHandlerWithConfig(t, Config{Metrics: m, Dedup: NewDedup(DefaultDedupCapacity, nil), WAL: wal})
	return h, wal
}

func newTestHandlerWithIndex(t *testing.T, m *metrics.Metrics, index *RecentIndex) (*Handler, *fakeWAL) {
	t.Helper()
	wal := &fakeWAL{}
	h := newTestHandlerWithConfig(t, Config{Metrics: m, Dedup: NewDedup(DefaultDedupCapacity, nil), WAL: wal, Index: index})
	return h, wal
}

func newTestHandlerWithWAL(t *testing.T, m *metrics.Metrics, wal WAL) *Handler {
	t.Helper()
	return newTestHandlerWithConfig(t, Config{Metrics: m, Dedup: NewDedup(DefaultDedupCapacity, nil), WAL: wal})
}

func newTestHandlerWithConfig(t *testing.T, cfg Config) *Handler {
	t.Helper()
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func post(t *testing.T, h *Handler, contentType, encoding, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postBytes(t, h, contentType, encoding, []byte(body))
}

func postBytes(t *testing.T, h *Handler, contentType, encoding string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	h.Events(rec, req)
	return rec
}

func validatePost(t *testing.T, h *Handler, contentType, encoding, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events/validate", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	h.Validate(rec, req)
	return rec
}

func expectEnvelopeStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) IngestEnvelope {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want %d body=%s", rec.Code, status, rec.Body.String())
	}
	var env IngestEnvelope
	decodeEnvelope(t, rec, &env)
	return env
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder, env *IngestEnvelope) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	if env.Rejected == nil {
		t.Fatal("rejected is nil")
	}
	if env.Deprecations == nil {
		t.Fatal("deprecations is nil")
	}
}

func validEventMap(id string) map[string]any {
	return map[string]any{
		"schema_version": "2.0",
		"event_id":       id,
		"ts_start":       "2026-04-25T14:00:00.000Z",
		"ts_end":         "2026-04-25T14:00:00.010Z",
		"duration_ms":    10,
		"kind":           "http",
		"service":        "checkout",
		"env":            "test",
		"trace_id":       "11111111111111111111111111111111",
		"span_id":        "1111111111111111",
		"parent_span_id": "",
		"status":         "ok",
		"steps": []any{
			map[string]any{"name": "db.load_cart", "start_ms": 0, "duration_ms": 4, "status": "ok"},
		},
		"logs":   []any{},
		"fields": map[string]any{"http": map[string]any{"method": "POST", "route": "/checkout", "status": 200}},
		"errors": []any{},
	}
}

func validEventJSON(id string) string {
	b, _ := json.Marshal(validEventMap(id))
	return string(b)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exactSizedJSON(t *testing.T, size int) string {
	t.Helper()
	raw := validEventMap("00000000-0000-4000-8000-000000000001")
	raw["padding"] = ""
	base := mustJSON(t, raw)
	if len(base) > size {
		t.Fatalf("base event size %d exceeds target %d", len(base), size)
	}
	raw["padding"] = strings.Repeat("a", size-len(base))
	out := mustJSON(t, raw)
	if len(out) != size {
		t.Fatalf("padded JSON size %d != target %d", len(out), size)
	}
	return out
}

func gzipBytes(body []byte) []byte {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	_, _ = gz.Write(body)
	_ = gz.Close()
	return out.Bytes()
}

func gatherMap(t *testing.T, reg *prometheus.Registry) map[string]*dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, mf := range families {
		out[mf.GetName()] = mf
	}
	return out
}

func histogramCount(mf *dto.MetricFamily) uint64 {
	if mf == nil || len(mf.GetMetric()) == 0 || mf.GetMetric()[0].GetHistogram() == nil {
		return 0
	}
	return mf.GetMetric()[0].GetHistogram().GetSampleCount()
}

func counterWithLabel(mf *dto.MetricFamily, label, value string) float64 {
	if mf == nil {
		return 0
	}
	for _, metric := range mf.GetMetric() {
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == label && lp.GetValue() == value && metric.GetCounter() != nil {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func counterValue(mf *dto.MetricFamily) float64 {
	if mf == nil || len(mf.GetMetric()) == 0 || mf.GetMetric()[0].GetCounter() == nil {
		return 0
	}
	return mf.GetMetric()[0].GetCounter().GetValue()
}

type fakeWAL struct {
	writes [][]byte
	failAt int64
	delay  time.Duration
	count  atomic.Int64
	mu     sync.Mutex
}

func (w *fakeWAL) WriteRaw(line []byte) error {
	if w.delay > 0 {
		time.Sleep(w.delay)
	}
	n := w.count.Add(1)
	if w.failAt > 0 && n == w.failAt {
		return errFakeWAL
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := append([]byte(nil), line...)
	w.writes = append(w.writes, cp)
	return nil
}

func (w *fakeWAL) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

var errFakeWAL = &fakeWALError{}

type fakeWALError struct{}

func (e *fakeWALError) Error() string { return "fake WAL failure" }

type panicProjector struct{}

func (panicProjector) Project(*eventv2.Event) { panic("boom") }
