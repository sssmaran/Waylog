package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func newIntegrationServer(t *testing.T) (*ingest.Server, *coldstore.Store, *coldstore.BatchWriter) {
	t.Helper()

	cs, err := coldstore.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	bw := coldstore.NewBatchWriter(cs, coldstore.BatchWriterConfig{
		FlushInterval: 100 * time.Millisecond,
		BatchSize:     1000,
	}, nil)
	bw.Start()
	t.Cleanup(func() { bw.Stop() })

	reg := tools.NewRegistry()
	if err := tools.RegisterGraphTools(reg); err != nil {
		t.Fatal(err)
	}

	dedup := ingest.NewDedupCache()

	srv := ingest.NewServer(ingest.ServerConfig{
		Store:         graphstore.NewStore(),
		AskRegistry:   reg,
		DedupCache:    dedup,
		ColdWriter:    bw,
		ColdStore:     cs,
		SampleRatePct: 100,
	})

	return srv, cs, bw
}

func ingestEvent(t *testing.T, srv *ingest.Server, ev event.WideEvent) int {
	t.Helper()
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Events(w, req)
	return w.Code
}

func ingestEvents(t *testing.T, srv *ingest.Server, events []event.WideEvent) {
	t.Helper()
	for i, ev := range events {
		code := ingestEvent(t, srv, ev)
		if code != http.StatusAccepted {
			t.Fatalf("event %d: expected 202, got %d", i, code)
		}
	}
}

func makeEvents(n int, service string, idOffset int, extra ...testutil.EventOption) []event.WideEvent {
	events := make([]event.WideEvent, n)
	for i := range events {
		base := []testutil.EventOption{
			testutil.WithService(service),
			testutil.WithTraceID(fmt.Sprintf("%032x", idOffset+i+1)),
			testutil.WithSpanID(fmt.Sprintf("%016x", idOffset+i+1)),
			testutil.WithUser("user-"+string(rune('a'+i%26)), "standard", "us-east"),
		}
		events[i] = testutil.MakeEvent(append(base, extra...)...)
	}
	return events
}

func makeHealthyEvents(n int, service string) []event.WideEvent {
	return makeEvents(n, service, 0,
		testutil.WithStatusCode(200),
		testutil.WithLatency(50),
	)
}

func makeFailureEvents(n int, service, errorCode string) []event.WideEvent {
	return makeEvents(n, service, 10000,
		testutil.WithError(errorCode, service+" error"),
		testutil.WithStatusCode(502),
		testutil.WithLatency(200),
	)
}

func httpGET(t *testing.T, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func httpPOST(t *testing.T, handler http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func httpPOSTWithHeaders(t *testing.T, handler http.HandlerFunc, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode json: %v\nbody: %s", err, w.Body.String())
	}
}

func flushColdWriter(t *testing.T, bw *coldstore.BatchWriter) {
	t.Helper()
	bw.Stop()
	time.Sleep(150 * time.Millisecond) // drain any in-flight batch
}
