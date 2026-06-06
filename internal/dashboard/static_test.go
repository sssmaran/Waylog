package dashboard

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticDashboardHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	html := string(body)

	required := []string{
		"<title>Crux Triage</title>",
		"fonts.googleapis.com/css2?family=Geist",
		"waylog-dashboard-theme",
		"data-theme",
		"id=\"theme-toggle\"",
		"Light theme",
		"Dark theme",
		"#FF7300",
		"#231F1C",
		"Find the failure that started the cascade.",
		"failure-path",
		"No failures in this window.",
		"http://localhost:9081/demo",
		"topbar-link",
		"Demo controls",
		"emptyStateCopy(\"recent requests\")",
		"#/errors",
		"#/explain",
		"#/blast",
		"#/incident",
		"/v1/incidents/active",
		"Active incidents",
		"No active incidents right now",
		"Crux is connected and watching for incident evidence.",
		"In the demo, the auto-fire loop opens one shortly.",
		"./scripts/demo-fire-burst.sh",
		"Send a wide event and watch the recent requests panel",
		"renderTriageKpis",
		"Highest cause",
		"Affected requests",
		"Since last incident",
		"All clear — Crux is watching for new error families.",
		"sortAlertsBySeverity",
		"Demo provider links may point back to demo controls",
		"Provider links open the alert source configured for this incident.",
		"data-retry-fetch",
		"Crux",
		"repeat(auto-fit, minmax(min(100%, 280px), 1fr))",
		".incident-card",
		"incident-body-grid",
		"min-width: 0",
		"overflow-wrap: anywhere",
		"flex-shrink: 0",
		"incident-meta",
		"Suggested checks",
		"Instrumentation warnings",
		"sample_traces",
		"first observable failing step",
		"Failure cascade from sampled trace",
		"trace-backed cascade",
		"renderAlertEvidenceBlock",
		"Alertmanager matches",
		"data-expanded=\"false\"",
		"data-alert-expand",
		"Show all",
		"Show fewer alerts",
		"affected_requests",
		"sampled_traces",
		"/v1/triage/{id}/report?format=markdown&snapshot=true",
		"copyReportMarkdown",
		"Report preview",
		"id=\"copy-toast\"",
		"role=\"status\"",
		"Markdown copied",
		"Could not copy markdown",
		"origin_service",
		"origin_step",
		"provider_url",
		"At open",
		"Top services",
		"capturedFooter(l, source, \"span\", \"Captured\")",
		"renderPropagationBlock",
		"renderBlastBlock",
		"renderRuntimeBlock",
		"Runtime evidence",
		"Infrastructure &amp; application failures",
		"data-runtime-row",
		"runtimeSubtypeLabel",
		"OOMKilled",
		"CrashLoopBackOff",
		"No correlated runtime events (pod restarts, OOMKills, panics)",
		"incident.runtime",
	}
	for _, needle := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("dashboard html missing %q", needle)
		}
	}

	// Required strings are exact source guards. Forbidden product claims are
	// case-insensitive so capitalization changes cannot bypass the copy rules.
	lowerHTML := strings.ToLower(html)
	forbidden := []string{
		"/ui/ask",
		"/ui/explain",
		"Chart(",
		"cytoscape(",
		"/v1/overview",
		"/v1/routes",
		"/v1/topology",
		"/v1/insight",
		"/ui/incidents",
		"No active incidents.",
		"Markdown artifact",
		"chart.umd.min.js",
		"chartjs-plugin-annotation.min.js",
		"cytoscape.min.js",
		"causal service graph",
		"complete propagation tree",
		"all downstream failures",
		"full topology",
	}
	for _, needle := range forbidden {
		if strings.Contains(lowerHTML, strings.ToLower(needle)) {
			t.Fatalf("dashboard html still references %q", needle)
		}
	}
}

// TestDashboardXSSDefenses pins the dashboard's XSS invariants so a future edit
// cannot silently drop escaping on attacker-influenceable event/incident fields.
// All dashboard data originates from ingested WideEvents and signals, which an
// untrusted service can populate with markup.
func TestDashboardXSSDefenses(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	html := string(body)

	// The escaping/URL primitives must exist and safeHTTPURL must enforce scheme.
	mustHave := []string{
		"function esc(",
		"function safeHTTPURL(",
		`u.protocol === "http:"`,
		`u.protocol === "https:"`,
		// Provider links are gated through safeHTTPURL before reaching an href.
		"safeHTTPURL(alert.provider_url)",
		"safeHTTPURL(a.provider_url)",
		// Attacker-influenceable fields are escaped at the HTML sink.
		"esc(alert.reason",
		"esc(alert.source",
		"esc(m.reason",
		"esc(m.subtype",
		"esc(m.service",
		// The triage report preview is injected as text, never HTML.
		"body.textContent = report",
		`<pre id="report-body"`,
	}
	for _, needle := range mustHave {
		if !strings.Contains(html, needle) {
			t.Fatalf("dashboard XSS defense missing %q", needle)
		}
	}

	// Raw, unescaped interpolation of user fields into HTML is forbidden. Current
	// code wraps every one of these in esc()/safeHTTPURL(), so these literals must
	// never appear; their presence signals a regressed sink. The needles omit the
	// closing brace on purpose so a regression with a fallback — e.g.
	// `${m.source || ""}` — is caught too, not just the bare `${m.source}`. The
	// safe form is always `${esc(field` / `${safeHTTPURL(field`, so the `${field`
	// prefix never matches correct code.
	forbidden := []string{
		"${alert.reason",
		"${alert.message",
		"${alert.source",
		"${alert.provider_url",
		"${m.reason",
		"${m.subtype",
		"${m.service",
		"${m.source",
		".innerHTML = report",
	}
	for _, needle := range forbidden {
		if strings.Contains(html, needle) {
			t.Fatalf("dashboard html has an unescaped user-data sink: %q", needle)
		}
	}
}

func TestVendoredDashboardBundlesRemoved(t *testing.T) {
	for _, name := range []string{
		"static/chart.umd.min.js",
		"static/chartjs-plugin-annotation.min.js",
		"static/cytoscape.min.js",
	} {
		if _, err := fs.Stat(staticFiles, name); err == nil {
			t.Fatalf("vendored dashboard bundle still embedded: %s", name)
		}
	}
}
