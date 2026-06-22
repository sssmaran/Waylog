package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

func sampleIncident() incidents.Incident {
	users := 8
	return incidents.Incident{
		IncidentID:       "inc_test123",
		Env:              "prod",
		Service:          "checkout",
		ErrorFamily:      apiv2.ErrorFamily{Service: "checkout", Step: "payment.charge", ErrorCode: "PMT_502"},
		Status:           incidents.StatusActive,
		Cause:            incidents.CauseDependency,
		Confidence:       incidents.ConfidenceHigh,
		Severity:         8,
		AffectedRequests: 12,
		AffectedUsers:    &users,
		AffectedServices: 4,
	}
}

// capture is a tiny recording HTTP server.
type capture struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func (c *capture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		c.mu.Lock()
		c.bodies = append(c.bodies, m)
		c.mu.Unlock()
		if c.status != 0 {
			w.WriteHeader(c.status)
		}
	}))
}

func (c *capture) last() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return nil
	}
	return c.bodies[len(c.bodies)-1]
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func TestDeliverPostsToAllConfigured(t *testing.T) {
	slack, pd, generic := &capture{}, &capture{}, &capture{}
	ss, ps, gs := slack.server(t), pd.server(t), generic.server(t)
	defer ss.Close()
	defer ps.Close()
	defer gs.Close()

	n := New(Config{
		SlackWebhookURL:     ss.URL,
		PagerDutyRoutingKey: "rk_123",
		GenericWebhookURL:   gs.URL,
		BaseURL:             "http://waylog.local",
	}, nil)
	n.pagerDutyURL = ps.URL // redirect PD to the test server

	n.deliver(sampleIncident(), actionOpened)

	if slack.count() != 1 || pd.count() != 1 || generic.count() != 1 {
		t.Fatalf("want 1 post each, got slack=%d pd=%d generic=%d", slack.count(), pd.count(), generic.count())
	}

	// Generic: raw incident facts.
	g := generic.last()
	if g["event"] != "opened" {
		t.Fatalf("generic event = %v, want opened", g["event"])
	}
	inc := g["incident"].(map[string]any)
	if inc["incident_id"] != "inc_test123" || inc["error_code"] != "PMT_502" {
		t.Fatalf("generic incident payload wrong: %v", inc)
	}

	// PagerDuty Events v2: trigger + dedup_key + routing_key.
	p := pd.last()
	if p["event_action"] != "trigger" || p["routing_key"] != "rk_123" || p["dedup_key"] != "inc_test123" {
		t.Fatalf("pagerduty payload wrong: %v", p)
	}

	// Slack: a Block Kit message mentioning the opened state + incident id somewhere.
	if slack.last()["blocks"] == nil {
		t.Fatalf("slack payload missing blocks: %v", slack.last())
	}
}

func TestResolveUsesResolveAction(t *testing.T) {
	pd, generic := &capture{}, &capture{}
	ps, gs := pd.server(t), generic.server(t)
	defer ps.Close()
	defer gs.Close()

	n := New(Config{PagerDutyRoutingKey: "rk", GenericWebhookURL: gs.URL}, nil)
	n.pagerDutyURL = ps.URL

	n.deliver(sampleIncident(), actionResolved)

	if pd.last()["event_action"] != "resolve" {
		t.Fatalf("pd event_action = %v, want resolve", pd.last()["event_action"])
	}
	if generic.last()["event"] != "resolved" {
		t.Fatalf("generic event = %v, want resolved", generic.last()["event"])
	}
}

func TestUnconfiguredIsNoOp(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("empty config must report Enabled()=false")
	}
	// Must not panic when nothing is configured.
	New(Config{}, nil).deliver(sampleIncident(), actionOpened)
}

func TestBestEffortContinuesWhenOneDestinationFails(t *testing.T) {
	failing := &capture{status: http.StatusInternalServerError}
	ok := &capture{}
	fs, oks := failing.server(t), ok.server(t)
	defer fs.Close()
	defer oks.Close()

	// Slack fails (500); generic must still receive its POST.
	n := New(Config{SlackWebhookURL: fs.URL, GenericWebhookURL: oks.URL}, nil)
	n.deliver(sampleIncident(), actionOpened)

	if failing.count() != 1 {
		t.Fatalf("failing destination should still be attempted once, got %d", failing.count())
	}
	if ok.count() != 1 {
		t.Fatalf("a destination failure must not skip the others, got %d", ok.count())
	}
}

func TestPagerDutySeverityMapping(t *testing.T) {
	cases := map[int]string{10: "critical", 8: "critical", 7: "error", 5: "error", 4: "warning", 3: "warning", 1: "info"}
	for sev, want := range cases {
		if got := pagerDutySeverity(sev); got != want {
			t.Fatalf("severity %d -> %q, want %q", sev, got, want)
		}
	}
}
