package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNew_RegistersCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	if m.ActiveRuns == nil {
		t.Fatal("ActiveRuns not initialized")
	}
	if m.IngestDuration == nil {
		t.Fatal("IngestDuration not initialized")
	}

	// verify Handler serves metrics
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestNew_NilRegistry(t *testing.T) {
	m := New(nil)
	if m == nil {
		t.Fatal("New(nil) should not return nil")
	}

	// should still serve metrics
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestHandler_ServesRegisteredMetrics(t *testing.T) {
	m := New(prometheus.NewRegistry())

	// increment a counter
	m.RunsTotal.WithLabelValues("completed").Inc()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "waylog_agent_runs_total") {
		t.Fatal("expected waylog_agent_runs_total in metrics output")
	}
}
