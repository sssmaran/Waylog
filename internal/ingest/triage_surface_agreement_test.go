package ingest_test

// Data-correctness invariant (g): for one incident built by one triage.Engine,
// every surface that emits a triage report must agree on the canonical
// report_hash. This exercises the REAL surfaces — the REST handler, the
// triage_incident tool, and the render_triage_report tool — and compares the
// hash each produces, catching surface-specific drift (an envelope that drops a
// hashed field, a re-marshal, or a stale embedded hash). It reuses the stub
// triage dependencies defined in triage_route_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/reports"
	"github.com/sssmaran/WaylogCLI/internal/tools"
	"github.com/sssmaran/WaylogCLI/internal/triage"
	"github.com/sssmaran/WaylogCLI/internal/triagehttp"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

func TestTriageSurfacesAgreeOnReportHash(t *testing.T) {
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
	ctx := context.Background()

	// --- REST surface: GET /v1/triage/{id} returns the Report as JSON ---
	mux := http.NewServeMux()
	mux.Handle("/v1/triage/", http.HandlerFunc(triagehttp.NewHandler(eng).Triage))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/triage/inc_abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("REST status = %d, want 200", resp.StatusCode)
	}
	var restReport pkgtriage.Report
	if err := json.NewDecoder(resp.Body).Decode(&restReport); err != nil {
		t.Fatalf("decode REST report: %v", err)
	}
	if restReport.ReportHash == "" {
		t.Fatalf("REST report missing report_hash")
	}
	// The REST surface must not desync the embedded hash from the canonical hash.
	recomputed, err := restReport.CanonicalHash()
	if err != nil {
		t.Fatalf("recompute canonical hash: %v", err)
	}
	if recomputed != restReport.ReportHash {
		t.Fatalf("REST embedded hash desynced from canonical: embedded=%q canonical=%q",
			restReport.ReportHash, recomputed)
	}

	// --- Tool surfaces: triage_incident (raw Report) + render_triage_report (markdown) ---
	reg := tools.NewRegistry()
	if err := tools.RegisterTriageTool(reg, eng); err != nil {
		t.Fatalf("register triage_incident: %v", err)
	}
	if err := tools.RegisterTriageReportTool(reg, eng); err != nil {
		t.Fatalf("register render_triage_report: %v", err)
	}

	out, err := reg.Call(ctx, "triage_incident", json.RawMessage(`{"incident_id":"inc_abc","window":"15m"}`))
	if err != nil {
		t.Fatalf("triage_incident call: %v", err)
	}
	toolReport, ok := out.(*pkgtriage.Report)
	if !ok {
		t.Fatalf("triage_incident returned %T, want *pkgtriage.Report", out)
	}

	rendered, err := reg.Call(ctx, "render_triage_report", json.RawMessage(`{"incident_id":"inc_abc","format":"markdown"}`))
	if err != nil {
		t.Fatalf("render_triage_report call: %v", err)
	}
	r, ok := rendered.(reports.Rendered)
	if !ok {
		t.Fatalf("render_triage_report returned %T, want reports.Rendered", rendered)
	}
	md, ok := r.Body.(string)
	if !ok {
		t.Fatalf("markdown body is %T, want string", r.Body)
	}

	// --- Agreement across all three surfaces ---
	if toolReport.ReportHash != restReport.ReportHash {
		t.Fatalf("triage_incident vs REST hash drift: tool=%q rest=%q",
			toolReport.ReportHash, restReport.ReportHash)
	}
	if !strings.Contains(md, restReport.ReportHash) {
		t.Fatalf("render_triage_report markdown must embed report_hash %q", restReport.ReportHash)
	}
}
