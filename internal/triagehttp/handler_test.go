package triagehttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/triage"
	"github.com/sssmaran/WaylogCLI/internal/triagehttp"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestTriageHandlerReturnsReport(t *testing.T) {
	eng := newTriageEngineForHandler(t)
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodGet, "/v1/triage/inc_abc", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var rep pkgtriage.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.IncidentRef.ID != "inc_abc" {
		t.Fatalf("got id %q want inc_abc", rep.IncidentRef.ID)
	}
}

func TestTriageHandlerHonorsSnapshotQuery(t *testing.T) {
	eng := newTriageEngineForHandler(t)
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodGet, "/v1/triage/inc_abc?snapshot=true&window=30m", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTriageHandlerRejectsMissingID(t *testing.T) {
	eng := newTriageEngineForHandler(t)
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodGet, "/v1/triage/", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing id, got %d", rr.Code)
	}
}

func TestTriageHandlerRejectsNonGET(t *testing.T) {
	eng := newTriageEngineForHandler(t)
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodPost, "/v1/triage/inc_abc", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST, got %d", rr.Code)
	}
}

func TestTriageHandlerUnknownIncidentIsNotFound(t *testing.T) {
	eng := newTriageEngineForHandlerWithIncidents(t, handlerUnknownIncidents{})
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodGet, "/v1/triage/inc_missing", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown incident, got %d; body=%s", rr.Code, rr.Body.String())
	}
}

func TestTriageReportHandlerRendersMarkdown(t *testing.T) {
	eng := newTriageEngineForHandler(t)
	h := triagehttp.NewHandler(eng)

	req := httptest.NewRequest(http.MethodGet, "/v1/triage/inc_abc/report?format=markdown", nil)
	rr := httptest.NewRecorder()
	h.Triage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Waylog Triage Report") || !strings.Contains(rr.Body.String(), "inc_abc") {
		t.Fatalf("unexpected report:\n%s", rr.Body.String())
	}
}

// helper: stub engine
func newTriageEngineForHandler(t *testing.T) *triage.Engine {
	return newTriageEngineForHandlerWithIncidents(t, handlerStubIncidents{})
}

func newTriageEngineForHandlerWithIncidents(t *testing.T, incidents triage.IncidentLookup) *triage.Engine {
	t.Helper()
	deps := triage.Deps{
		Incidents:  incidents,
		Blast:      handlerStubBlast{},
		Story:      handlerStubStory{},
		Signals:    handlerStubSignals{},
		NextChecks: handlerStubNextChecks{},
		Now:        func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) },
	}
	eng, err := triage.NewEngine(deps)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return eng
}

type handlerStubIncidents struct{}

func (handlerStubIncidents) GetIncident(ctx context.Context, id string) (triage.IncidentSummary, error) {
	return triage.IncidentSummary{ID: id, Window: "15m", Confidence: pkgtriage.ConfidenceMedium}, nil
}

type handlerUnknownIncidents struct{}

func (handlerUnknownIncidents) GetIncident(ctx context.Context, id string) (triage.IncidentSummary, error) {
	return triage.IncidentSummary{}, triage.ErrUnknownIncident
}

type handlerStubBlast struct{}

func (handlerStubBlast) BlastSnapshot(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) (triage.BlastSnapshotResult, error) {
	return triage.BlastSnapshotResult{}, nil
}

type handlerStubStory struct{}

func (handlerStubStory) FirstFailureStory(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) (triage.FirstFailureResult, error) {
	return triage.FirstFailureResult{}, nil
}

type handlerStubSignals struct{}

func (handlerStubSignals) SignalsFor(ctx context.Context, inc triage.IncidentSummary, opts triage.BuildOptions) ([]triage.SignalEvidence, error) {
	return nil, nil
}

type handlerStubNextChecks struct{}

func (handlerStubNextChecks) NextChecks(ctx context.Context, inc triage.IncidentSummary) ([]triage.NextCheckSpec, error) {
	return nil, nil
}
