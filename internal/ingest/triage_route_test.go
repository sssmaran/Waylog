package ingest_test

// Integration test for Task 11: verifies that the /v1/triage/{id} route is
// dispatched to the triage handler when wired into a ServeMux the same way
// cmd/ingest/main.go wires it. The Server type does not own this route
// (cmd/ingest/main.go composes the mux directly), so this test reproduces
// the exact mount pattern with stubbed triage dependencies.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/triage"
	"github.com/sssmaran/WaylogCLI/internal/triagehttp"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestTriageRouteDispatchesToHandler(t *testing.T) {
	eng, err := triage.NewEngine(triage.Deps{
		Incidents:  stubTriageIncidents{},
		Blast:      stubTriageBlast{},
		Story:      stubTriageStory{},
		Signals:    stubTriageSignals{},
		NextChecks: stubTriageNextChecks{},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	h := triagehttp.NewHandler(eng)

	// Mirror cmd/ingest/main.go: mux.Handle("/v1/triage/", readCORS(h.Triage)).
	// We omit auth here because the auth wrapper is exercised elsewhere; this
	// test verifies the dispatch wiring (path → handler).
	mux := http.NewServeMux()
	mux.Handle("/v1/triage/", http.HandlerFunc(h.Triage))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/triage/inc_abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("route not registered (404)")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("Content-Type = %q, want json", ct)
	}
}

// --- stub dependencies ---

type stubTriageIncidents struct{}

func (stubTriageIncidents) GetIncident(_ context.Context, id string) (triage.IncidentSummary, error) {
	return triage.IncidentSummary{ID: id, Window: "15m", Confidence: pkgtriage.ConfidenceMedium}, nil
}

type stubTriageBlast struct{}

func (stubTriageBlast) BlastSnapshot(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) (triage.BlastSnapshotResult, error) {
	return triage.BlastSnapshotResult{}, nil
}

type stubTriageStory struct{}

func (stubTriageStory) FirstFailureStory(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) (triage.FirstFailureResult, error) {
	return triage.FirstFailureResult{}, nil
}

type stubTriageSignals struct{}

func (stubTriageSignals) SignalsFor(_ context.Context, _ triage.IncidentSummary, _ triage.BuildOptions) ([]triage.SignalEvidence, error) {
	return nil, nil
}

type stubTriageNextChecks struct{}

func (stubTriageNextChecks) NextChecks(_ context.Context, _ triage.IncidentSummary) ([]triage.NextCheckSpec, error) {
	return nil, nil
}
