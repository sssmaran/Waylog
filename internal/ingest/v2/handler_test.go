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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sssmaran/WaylogCLI/internal/auth"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
)

func TestEventsSingleJSONValid(t *testing.T) {
	h := newTestHandler(t, nil)
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
}

func TestEventsRejectsV1BridgeNotImplemented(t *testing.T) {
	raw := validEventMap("00000000-0000-4000-8000-000000000001")
	raw["schema_version"] = "1.1"

	rec := post(t, newTestHandler(t, nil), "application/json", "", mustJSON(t, raw))
	env := expectEnvelopeStatus(t, rec, http.StatusOK)
	if env.Accepted != 0 || len(env.Rejected) != 1 || env.Rejected[0].Reason != ReasonBridgeNotImplemented {
		t.Fatalf("env=%+v", env)
	}
}

func TestEventsUnsupportedContentEncoding(t *testing.T) {
	for _, encoding := range []string{"br", "deflate", "compress"} {
		t.Run(encoding, func(t *testing.T) {
			rec := post(t, newTestHandler(t, nil), "application/json", encoding, validEventJSON("00000000-0000-4000-8000-000000000001"))
			env := expectEnvelopeStatus(t, rec, http.StatusBadRequest)
			if env.Rejected[0].Reason != ReasonUnsupportedEncoding {
				t.Fatalf("reason=%q", env.Rejected[0].Reason)
			}
		})
	}
}

func TestEventsBodySizeBoundaries(t *testing.T) {
	exact := exactSizedJSON(t, maxBodyBytes)
	rec := post(t, newTestHandler(t, nil), "application/json", "", exact)
	env := expectEnvelopeStatus(t, rec, http.StatusOK)
	if env.Accepted != 1 {
		t.Fatalf("accepted=%d body=%s", env.Accepted, rec.Body.String())
	}

	tooLarge := exactSizedJSON(t, maxBodyBytes+1)
	rec = post(t, newTestHandler(t, nil), "application/json", "", tooLarge)
	env = expectEnvelopeStatus(t, rec, http.StatusRequestEntityTooLarge)
	if env.Rejected[0].Reason != ReasonBodyOversize {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}

	rec = postBytes(t, newTestHandler(t, nil), "application/json", "gzip", gzipBytes([]byte(tooLarge)))
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
			rec := post(t, newTestHandler(t, nil), "application/json", "", mustJSON(t, raw))
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
	h := newTestHandler(t, nil)
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
	if env.Accepted != 3 {
		t.Fatalf("env=%+v", env)
	}
}

func TestEventsBatchOversize(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxBatchItems+1; i++ {
		b.WriteString(validEventJSON(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)))
		b.WriteByte('\n')
	}
	env := expectEnvelopeStatus(t, post(t, newTestHandler(t, nil), "application/x-ndjson", "", b.String()), http.StatusRequestEntityTooLarge)
	if env.Rejected[0].Reason != ReasonBatchOversize {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsGzipInvalidBody(t *testing.T) {
	rec := post(t, newTestHandler(t, nil), "application/x-ndjson", "gzip", "not gzip")
	env := expectEnvelopeStatus(t, rec, http.StatusBadRequest)
	if env.Rejected[0].Reason != ReasonInvalidBody {
		t.Fatalf("reason=%q", env.Rejected[0].Reason)
	}
}

func TestEventsContentTypeHandling(t *testing.T) {
	h := newTestHandler(t, nil)
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
	h := newTestHandler(t, nil)
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
	h := newTestHandler(t, nil)
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
	h := newTestHandler(t, m)

	for _, n := range []int{1, 16, 256} {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(validEventJSON(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)))
			b.WriteByte('\n')
		}
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
}

func newTestHandler(t *testing.T, m *metrics.Metrics) *Handler {
	t.Helper()
	h, err := New(m)
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
