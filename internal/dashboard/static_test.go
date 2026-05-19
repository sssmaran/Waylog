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
		"<title>Waylog v2 Triage</title>",
		"fonts.googleapis.com/css2?family=Geist",
		"waylog-dashboard-theme",
		"data-theme",
		"id=\"theme-toggle\"",
		"Light theme",
		"Dark theme",
		"Find the failure that started the cascade.",
		"First failing step",
		"failure-path",
		"No failures in this window.",
		"http://localhost:9081/demo",
		"topbar-link",
		"Demo controls",
		"No recent requests yet",
		"Run a scenario",
		"#/errors",
		"#/explain",
		"#/blast",
		"#/incident",
		"/v1/incidents/active",
		"Active incidents",
		"No active incidents.",
		"repeat(auto-fit, minmax(min(100%, 280px), 1fr))",
		".incident-card",
		"min-width: 0",
		"overflow-wrap: anywhere",
		"flex-shrink: 0",
		"incident-meta",
		"Next checks",
		"Instrumentation warnings",
		"sample_traces",
		"renderSparkline",
		"This dashboard requires WAYLOG_V2_READS=true",
		"first observable failing step",
		"Where did it start?",
		"How bad is it?",
		"At open",
		"Top services",
		"captured ",
		"renderPropagationBlock",
		"renderBlastBlock",
	}
	for _, needle := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("dashboard html missing %q", needle)
		}
	}

	forbidden := []string{
		"/ui/ask",
		"/ui/explain",
		"Chart(",
		"cytoscape(",
		"/v1/overview",
		"/v1/routes",
		"/v1/topology",
		"/v1/insight",
		"chart.umd.min.js",
		"chartjs-plugin-annotation.min.js",
		"cytoscape.min.js",
	}
	for _, needle := range forbidden {
		if strings.Contains(html, needle) {
			t.Fatalf("dashboard html still references %q", needle)
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
