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
		"Simulate a checkout outage. Watch Waylog explain the cascade.",
		"Run payment outage",
		"Run happy checkout",
		"Run suppressed known issue",
		"Run cart not found",
		"Run checkout 500",
		"Run traffic burst",
		"Production-like traffic mix",
		"posts demo deploy/dependency signals",
		"active incident",
		"Burst captured",
		"Open dashboard",
		"Explain this trace",
		"View impact",
		"Still propagating through Waylog…",
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
	}
	for _, needle := range forbidden {
		if strings.Contains(html, needle) {
			t.Fatalf("demo UI still has terminal-first copy %q", needle)
		}
	}
}
