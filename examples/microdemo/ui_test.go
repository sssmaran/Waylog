package microdemo_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
)

func TestDemoUIProductShowcaseCopy(t *testing.T) {
	gateway := microdemo.NewGatewayHandler("http://checkout.example")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/demo", nil)
	gateway.ServeDemo(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	html := rec.Body.String()
	required := []string{
		"<title>Crux — Live demo</title>",
		"<span class=\"name\">Crux</span>",
		"#FF7300",
		"--ok: #15803d",
		".proof-checklist .ok { color: var(--ok);",
		"failure cascade",
		"blast radius",
		"alert evidence",
		"triage report",
		"Simulate a checkout outage. Watch Crux explain the cascade.",
		"Run payment outage",
		"Run happy checkout",
		"Run suppressed known issue",
		"Run cart not found",
		"Run checkout 500",
		"Run inventory outage",
		"Run traffic burst",
		"Run proof loop",
		"Alert-to-report proof",
		"alert → incident → triage → reports → scorecard",
		"Alert correlated. Root cause identified. Report verified.",
		"checkout:payment.charge:PMT_502",
		"Proof checklist",
		"Alert accepted",
		"Incident opened",
		"Triage built",
		"Read/tool/plan hashes agree",
		"Hash agreement across triage surfaces",
		"read endpoint",
		"direct tool",
		"plan template",
		"repeat snapshot",
		"Evidence IDs",
		"Evidence completeness",
		"Operator report",
		"Stable report hash",
		"Inflation avoided",
		"Slack JSON",
		"PagerDuty note",
		"shortHashValue",
		"humanBool",
		"#/incident/",
		"Production-like traffic mix",
		"posts demo deploy/dependency signals",
		"active incident",
		"Burst captured",
		"Proof loop complete",
		"Open dashboard",
		"Explain this trace",
		"View impact",
		"Still propagating through Crux…",
		"In this local demo, provider links open demo controls.",
		"Happy checkout captured",
		"Payment outage captured",
		"Cart not found captured",
		"Checkout 500 captured",
		"Suppressed issue captured",
	}
	for _, needle := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("demo UI missing %q", needle)
		}
	}
	forbidden := []string{
		"copy the returned",
		"waylog explain &lt;trace_id&gt;",
		"waylog errors",
		"waylog blast",
		"Watch Waylog explain",
		"Still propagating through Waylog",
		"<title>Waylog — Live demo</title>",
		"#22c55e",
		"rgba(34, 197, 94",
		"rgba(94, 255, 139",
	}
	for _, needle := range forbidden {
		if strings.Contains(html, needle) {
			t.Fatalf("demo UI still has terminal-first copy %q", needle)
		}
	}
}
