// Package notify sends outbound incident notifications (Slack, PagerDuty Events
// v2, generic webhook) when the incident engine opens or resolves an incident.
// It implements incidents.Notifier. All destinations are opt-in; with none
// configured the notifier is a no-op. Delivery is best-effort and non-blocking:
// a failed or slow destination never blocks the engine tick or skips the others.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
)

const (
	actionOpened   = "opened"
	actionResolved = "resolved"

	pagerDutyEventsURL = "https://events.pagerduty.com/v2/enqueue"
	deliverTimeout     = 5 * time.Second
)

// Config holds the outbound destinations. Each is optional.
type Config struct {
	SlackWebhookURL     string // Slack incoming webhook URL
	PagerDutyRoutingKey string // PagerDuty Events API v2 routing key
	GenericWebhookURL   string // arbitrary endpoint receiving raw incident JSON
	BaseURL             string // public base URL for dashboard links (optional)
}

// Enabled reports whether at least one destination is configured.
func (c Config) Enabled() bool {
	return c.SlackWebhookURL != "" || c.PagerDutyRoutingKey != "" || c.GenericWebhookURL != ""
}

// Notifier delivers incident lifecycle events to the configured destinations.
type Notifier struct {
	cfg          Config
	client       *http.Client
	log          *slog.Logger
	pagerDutyURL string
}

// New builds a Notifier. Wire it onto the engine only when cfg.Enabled().
func New(cfg Config, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		cfg:          cfg,
		client:       &http.Client{Timeout: deliverTimeout},
		log:          log,
		pagerDutyURL: pagerDutyEventsURL,
	}
}

// IncidentOpened implements incidents.Notifier (non-blocking, best-effort).
func (n *Notifier) IncidentOpened(inc incidents.Incident) { go n.deliver(inc, actionOpened) }

// IncidentResolved implements incidents.Notifier (non-blocking, best-effort).
func (n *Notifier) IncidentResolved(inc incidents.Incident) { go n.deliver(inc, actionResolved) }

// deliver posts to every configured destination. Synchronous (the exported
// methods wrap it in a goroutine); each destination is independent so one
// failure never skips the rest.
func (n *Notifier) deliver(inc incidents.Incident, action string) {
	if n.cfg.SlackWebhookURL != "" {
		n.post("slack", n.cfg.SlackWebhookURL, slackPayload(inc, action, n.cfg.BaseURL))
	}
	if n.cfg.PagerDutyRoutingKey != "" {
		n.post("pagerduty", n.pagerDutyURL, pagerDutyPayload(inc, action, n.cfg.PagerDutyRoutingKey, n.cfg.BaseURL))
	}
	if n.cfg.GenericWebhookURL != "" {
		n.post("webhook", n.cfg.GenericWebhookURL, genericPayload(inc, action, n.cfg.BaseURL))
	}
}

func (n *Notifier) post(dest, url string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		n.log.Warn("notify: marshal failed", "dest", dest, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.log.Warn("notify: request build failed", "dest", dest, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Warn("notify: post failed", "dest", dest, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("notify: destination returned error", "dest", dest, "status", resp.StatusCode)
	}
}

// ---- payload builders ----

func genericPayload(inc incidents.Incident, action, baseURL string) map[string]any {
	return map[string]any{
		"event": action,
		"incident": map[string]any{
			"incident_id":       inc.IncidentID,
			"env":               inc.Env,
			"service":           inc.ErrorFamily.Service,
			"step":              inc.ErrorFamily.Step,
			"error_code":        inc.ErrorFamily.ErrorCode,
			"status":            string(inc.Status),
			"cause":             string(inc.Cause),
			"confidence":        string(inc.Confidence),
			"severity":          inc.Severity,
			"affected_requests": inc.AffectedRequests,
			"affected_users":    affectedUsers(inc),
			"affected_services": inc.AffectedServices,
			"url":               incidentURL(baseURL),
		},
	}
}

func slackPayload(inc incidents.Incident, action, baseURL string) map[string]any {
	heading := "🔴 Incident opened"
	if action == actionResolved {
		heading = "✅ Incident resolved"
	}
	fields := []map[string]string{
		{"type": "mrkdwn", "text": "*Service*\n" + nz(inc.ErrorFamily.Service)},
		{"type": "mrkdwn", "text": "*Error*\n" + nz(inc.ErrorFamily.Step) + " / " + nz(inc.ErrorFamily.ErrorCode)},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Cause*\n%s (%s)", nz(string(inc.Cause)), nz(string(inc.Confidence)))},
		{"type": "mrkdwn", "text": fmt.Sprintf("*Impact*\n%d reqs · %d users · %d svcs", inc.AffectedRequests, affectedUsers(inc), inc.AffectedServices)},
		{"type": "mrkdwn", "text": "*Incident*\n`" + nz(inc.IncidentID) + "`"},
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]string{"type": "plain_text", "text": heading}},
		{"type": "section", "fields": fields},
	}
	if url := incidentURL(baseURL); url != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": "<" + url + "|Open dashboard>"},
		})
	}
	return map[string]any{"blocks": blocks}
}

func pagerDutyPayload(inc incidents.Incident, action, routingKey, baseURL string) map[string]any {
	eventAction := "trigger"
	if action == actionResolved {
		eventAction = "resolve"
	}
	out := map[string]any{
		"routing_key":  routingKey,
		"event_action": eventAction,
		"dedup_key":    inc.IncidentID,
	}
	// PagerDuty requires payload only for trigger; resolve needs just the dedup key.
	if eventAction == "trigger" {
		out["payload"] = map[string]any{
			"summary": fmt.Sprintf("%s/%s/%s (%s)", nz(inc.ErrorFamily.Service), nz(inc.ErrorFamily.Step),
				nz(inc.ErrorFamily.ErrorCode), nz(string(inc.Cause))),
			"source":   nz(inc.ErrorFamily.Service),
			"severity": pagerDutySeverity(inc.Severity),
			"custom_details": map[string]any{
				"incident_id":       inc.IncidentID,
				"env":               inc.Env,
				"cause":             string(inc.Cause),
				"confidence":        string(inc.Confidence),
				"affected_requests": inc.AffectedRequests,
				"affected_users":    affectedUsers(inc),
				"affected_services": inc.AffectedServices,
				"url":               incidentURL(baseURL),
			},
		}
	}
	return out
}

// pagerDutySeverity maps the incident's 1-10 severity to a PagerDuty severity.
func pagerDutySeverity(sev int) string {
	switch {
	case sev >= 8:
		return "critical"
	case sev >= 5:
		return "error"
	case sev >= 3:
		return "warning"
	default:
		return "info"
	}
}

func affectedUsers(inc incidents.Incident) int {
	if inc.AffectedUsers == nil {
		return 0
	}
	return *inc.AffectedUsers
}

func incidentURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/ui/"
}

func nz(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
