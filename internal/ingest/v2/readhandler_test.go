package ingestv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

func TestReadHandlerEventGetAndNotFound(t *testing.T) {
	h := newTestReadHandler(t, nil)
	h.reader.index.Insert(testTraceEvent("event-1", "trace", "svc", eventv2.StatusSuppressed, testTime(0)))

	rec := readGet(t, h.EventByID, "/v1/events/event-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ok eventGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatal(err)
	}
	if ok.Event.EventID != "event-1" || ok.Event.Status != eventv2.StatusSuppressed {
		t.Fatalf("event=%+v", ok.Event)
	}

	rec = readGet(t, h.EventByID, "/v1/events/missing")
	expectReadError(t, rec, http.StatusNotFound, errorCodeNotFound)
}

func TestReadHandlerSearchRejectsBadParamsAndReturnsNullCursor(t *testing.T) {
	h := newTestReadHandler(t, nil)
	rec := readGet(t, h.EventSearch, "/v1/events/search?foo=bar")
	expectReadError(t, rec, http.StatusBadRequest, errorCodeBadRequest)

	rec = readGet(t, h.EventSearch, "/v1/events/search?limit=201")
	expectReadError(t, rec, http.StatusBadRequest, errorCodeOverLimit)

	rec = readGet(t, h.EventSearch, "/v1/events/search")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["events"]) != "[]" || string(body["next_cursor"]) != "null" {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestReadHandlerRejectsWindowOverHotWindowAndOldSince(t *testing.T) {
	h := newTestReadHandler(t, nil)
	rec := readGet(t, h.EventSearch, "/v1/events/search?window=25h")
	expectReadError(t, rec, http.StatusBadRequest, errorCodeOverLimit)

	old := testTime(0).Add(-25 * time.Hour).Format(time.RFC3339Nano)
	rec = readGet(t, h.EventSearch, "/v1/events/search?since="+old)
	expectReadError(t, rec, http.StatusBadRequest, errorCodeOverLimit)
}

func TestReadHandlerTraceGetAndRecent(t *testing.T) {
	h := newTestReadHandler(t, nil)
	root := testTraceEvent("root", "trace", "gateway", eventv2.StatusOK, testTime(0))
	root.Steps = []eventv2.Step{{Name: "call.payment", SpanID: "span-payment", Status: "ok"}}
	payment := testTraceEvent("payment", "trace", "payment", eventv2.StatusError, testTime(1))
	payment.ParentSpanID = "span-payment"
	payment.Anchor = &eventv2.Anchor{Step: "charge", ErrorCode: "PMT_502"}
	h.reader.index.Insert(payment)
	h.reader.index.Insert(root)

	rec := readGet(t, h.TraceByID, "/v1/traces/trace")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var trace traceGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Linkage != LinkageCausal || ids(trace.Events) != "root,payment" {
		t.Fatalf("trace=%+v", trace)
	}

	rec = readGet(t, h.RecentTraces, "/v1/traces/recent?service=payment")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var recent recentTracesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &recent); err != nil {
		t.Fatal(err)
	}
	if len(recent.Traces) != 1 || recent.Traces[0].Status != eventv2.StatusError {
		t.Fatalf("recent=%+v", recent)
	}
}

func TestReadHandlerCORSPreflightAndPrefixRouteSafety(t *testing.T) {
	h := newTestReadHandler(t, nil)
	mux := http.NewServeMux()
	wrap := func(fn http.HandlerFunc) http.Handler {
		return http.HandlerFunc(ingest.CORSWrap("*", "GET, OPTIONS", fn))
	}
	mux.Handle("/v1/events/search", wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	mux.Handle("/v1/events/", wrap(h.EventByID))
	mux.Handle("/v1/traces/recent", wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	mux.Handle("/v1/traces/", wrap(h.TraceByID))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/events/event-1", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/events/search", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("search route captured by prefix: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/traces/recent", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("recent route captured by prefix: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReadHandlerMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	h := newTestReadHandler(t, m)
	readGet(t, h.EventByID, "/v1/events/missing")
	readGet(t, h.EventSearch, "/v1/events/search")

	fm := gatherMap(t, reg)
	if got := counterWithLabel(fm["waylog_v2_read_not_found_total"], "handler", readHandlerEventGet); got != 1 {
		t.Fatalf("not_found=%v want 1", got)
	}
	if got := counterWithLabel(fm["waylog_v2_read_empty_total"], "handler", readHandlerEventSearch); got != 1 {
		t.Fatalf("empty=%v want 1", got)
	}
	if got := histogramCountWithLabel(fm["waylog_v2_read_latency_seconds"], "handler", readHandlerEventGet); got != 2 {
		t.Fatalf("event_get latency count=%v want 2", got)
	}
	if got := histogramCountWithLabel(fm["waylog_v2_read_latency_seconds"], "handler", readHandlerEventSearch); got != 2 {
		t.Fatalf("event_search latency count=%v want 2", got)
	}
}

func newTestReadHandler(t *testing.T, m *metrics.Metrics) *ReadHandler {
	t.Helper()
	idx := NewRecentIndex(nil)
	h := NewReadHandler(NewReader(idx), m, 24*time.Hour)
	h.now = func() time.Time { return testTime(0).Add(24 * time.Hour) }
	return h
}

func readGet(t *testing.T, h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	h(rec, req)
	return rec
}

func expectReadError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want %d body=%s", rec.Code, status, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content-type=%q", rec.Header().Get("Content-Type"))
	}
	var body readErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
	if body.Error.Code != code {
		t.Fatalf("code=%q want %q body=%s", body.Error.Code, code, rec.Body.String())
	}
}

func histogramCountWithLabel(mf *dto.MetricFamily, label, value string) uint64 {
	if mf == nil {
		return 0
	}
	for _, metric := range mf.GetMetric() {
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == label && lp.GetValue() == value && metric.GetHistogram() != nil {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}
