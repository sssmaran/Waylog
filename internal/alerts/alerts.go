package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

const (
	CodeInvalidJSON       = "INVALID_JSON"
	CodeMissingFields     = "MISSING_FIELDS"
	CodeUnsupportedAlert  = "UNSUPPORTED_ALERT"
	CodeSignalUnavailable = "SIGNALS_UNAVAILABLE"
	CodeInternal          = "INTERNAL"
)

type IncidentSource interface {
	Active(ctx context.Context) ([]incidents.Incident, error)
	Get(ctx context.Context, id string) (incidents.Incident, error)
}

type TraceResolver interface {
	TraceStoryByTraceID(traceID string) (apiv2.StoryResponse, bool)
}

type Handler struct {
	store       signals.Store
	incidents   IncidentSource
	traces      TraceResolver
	now         func() time.Time
	matchWindow time.Duration
	maxBody     int64
}

type MatchResult struct {
	Matched    bool   `json:"matched"`
	IncidentID string `json:"incident_id,omitempty"`
	Strategy   string `json:"strategy"`
}

func NewHandler(store signals.Store, incidents IncidentSource, traces TraceResolver, matchWindow time.Duration) *Handler {
	if store == nil {
		store = signals.UnavailableStore{}
	}
	if matchWindow <= 0 {
		matchWindow = 15 * time.Minute
	}
	if matchWindow > 24*time.Hour {
		matchWindow = 24 * time.Hour
	}
	return &Handler{
		store:       store,
		incidents:   incidents,
		traces:      traces,
		now:         time.Now,
		matchWindow: matchWindow,
		maxBody:     1 << 20,
	}
}

func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, "invalid body", err.Error())
		return
	}
	sig, err := Normalize(body, h.now().UTC())
	if err != nil {
		var normErr *NormalizeError
		if errors.As(err, &normErr) {
			writeError(w, normErr.Status, normErr.Code, normErr.Message, normErr.Detail)
			return
		}
		writeError(w, http.StatusBadRequest, CodeUnsupportedAlert, "unsupported alert", err.Error())
		return
	}
	match := h.Match(r.Context(), sig)
	sig.SignalID = signals.NewSignalID()
	sig.ReceivedAt = h.now().UTC()
	if err := signals.Validate(sig, h.now().UTC(), 5*time.Minute); err != nil {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "invalid alert", err.Error())
		return
	}
	if err := h.store.Insert(r.Context(), sig); err != nil {
		if errors.Is(err, signals.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, CodeSignalUnavailable, "signals unavailable", "set SQLITE_PATH to enable alerts")
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"signal": sig, "match": match})
}

func (h *Handler) Match(ctx context.Context, sig *signals.Signal) MatchResult {
	if h.incidents == nil || sig == nil {
		return MatchResult{Strategy: "none"}
	}
	if id := metaString(sig.Metadata, "incident_id"); id != "" {
		if inc, err := h.incidents.Get(ctx, id); err == nil {
			return MatchResult{Matched: true, IncidentID: inc.IncidentID, Strategy: "incident_id"}
		}
	}
	if traceID := metaString(sig.Metadata, "trace_id"); traceID != "" && h.traces != nil {
		if story, ok := h.traces.TraceStoryByTraceID(traceID); ok && story.Anchor != nil {
			if inc, ok := h.findActive(ctx, sig.Env, story.Service, story.Anchor.ErrorCode, sig.Timestamp); ok {
				return MatchResult{Matched: true, IncidentID: inc.IncidentID, Strategy: "trace_id"}
			}
		}
	}
	if code := metaString(sig.Metadata, "error_code"); code != "" {
		if inc, ok := h.findActive(ctx, sig.Env, sig.Service, code, sig.Timestamp); ok {
			return MatchResult{Matched: true, IncidentID: inc.IncidentID, Strategy: "family"}
		}
	}
	return MatchResult{Strategy: "none"}
}

func (h *Handler) findActive(ctx context.Context, env, service, errorCode string, ts time.Time) (incidents.Incident, bool) {
	rows, err := h.incidents.Active(ctx)
	if err != nil {
		return incidents.Incident{}, false
	}
	for _, inc := range rows {
		if inc.Status != incidents.StatusActive {
			continue
		}
		if env != "" && inc.Env != "" && inc.Env != env {
			continue
		}
		if inc.ErrorFamily.Service != service || inc.ErrorFamily.ErrorCode != errorCode {
			continue
		}
		if !ts.IsZero() {
			lo := inc.StartedAt.Add(-h.matchWindow)
			hi := inc.UpdatedAt.Add(h.matchWindow)
			if ts.Before(lo) || ts.After(hi) {
				continue
			}
		}
		return inc, true
	}
	return incidents.Incident{}, false
}

type NormalizeError struct {
	Status  int
	Code    string
	Message string
	Detail  string
}

func (e *NormalizeError) Error() string {
	return e.Message
}

func Normalize(body []byte, now time.Time) (*signals.Signal, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, &NormalizeError{Status: http.StatusBadRequest, Code: CodeInvalidJSON, Message: "invalid json", Detail: err.Error()}
	}
	if source := getString(root, "source"); source != "" && source != "waylog" {
		return normalizeWaylog(root, now)
	}
	switch {
	case has(root, "ruleId") || has(root, "ruleUID") || has(root, "evalMatches") || isGrafanaAlertmanagerPayload(root):
		return normalizeGrafana(root, now)
	case has(root, "source") || has(root, "alert_id"):
		return normalizeWaylog(root, now)
	case has(root, "alerts") && (has(root, "receiver") || has(root, "commonLabels")):
		return normalizeAlertmanager(root, now)
	case has(root, "messages") || has(root, "event"):
		return normalizePagerDuty(root, now)
	default:
		return nil, &NormalizeError{Status: http.StatusBadRequest, Code: CodeUnsupportedAlert, Message: "unsupported alert", Detail: "expected waylog, alertmanager, grafana, or pagerduty payload"}
	}
}

func isGrafanaAlertmanagerPayload(root map[string]json.RawMessage) bool {
	if has(root, "orgId") || has(root, "orgID") {
		return true
	}
	alert := firstObject(root, "alerts")
	if has(alert, "dashboardURL") || has(alert, "panelURL") || has(alert, "ruleURL") {
		return true
	}
	labels := objectField(alert, "labels")
	annotations := objectField(alert, "annotations")
	return stringMapField(labels, "grafana_folder") != "" ||
		stringMapField(annotations, "__dashboardUid__") != "" ||
		stringMapField(annotations, "__panelId__") != ""
}

func normalizeWaylog(root map[string]json.RawMessage, now time.Time) (*signals.Signal, error) {
	src := getString(root, "source")
	if src == "" {
		src = "waylog"
	}
	ts := getTime(root, "timestamp", now)
	meta := baseMeta(root, src)
	return finalize(src, getString(root, "service"), getString(root, "env"), getSeverity(root, "severity"), getString(root, "reason"), getString(root, "message"), ts, meta)
}

func normalizeAlertmanager(root map[string]json.RawMessage, now time.Time) (*signals.Signal, error) {
	alert := firstObject(root, "alerts")
	labels := objectField(alert, "labels")
	annotations := objectField(alert, "annotations")
	meta := map[string]any{"raw_source": "alertmanager"}
	put(meta, "alert_id", firstString(alert, labels, "fingerprint", "alertname"))
	put(meta, "fingerprint", firstString(alert, labels, "fingerprint"))
	put(meta, "provider_url", firstString(alert, annotations, "generatorURL", "runbook_url"))
	put(meta, "error_code", stringMapField(labels, "error_code"))
	ts := timeField(alert, "startsAt", now)
	reason := firstNonEmpty(stringMapField(annotations, "summary"), stringMapField(annotations, "description"), stringMapField(labels, "alertname"))
	return finalize("alertmanager", stringMapField(labels, "service"), stringMapField(labels, "env"), severityFromString(stringMapField(labels, "severity")), reason, stringMapField(annotations, "description"), ts, meta)
}

func normalizeGrafana(root map[string]json.RawMessage, now time.Time) (*signals.Signal, error) {
	alert := firstObject(root, "alerts")
	labels := objectField(alert, "labels")
	annotations := objectField(alert, "annotations")
	if len(alert) == 0 {
		alert = root
	}
	meta := map[string]any{"raw_source": "grafana"}
	put(meta, "alert_id", firstNonEmpty(getString(root, "ruleUID"), getString(root, "ruleId"), stringMapField(labels, "alertname")))
	put(meta, "fingerprint", stringMapField(alert, "fingerprint"))
	put(meta, "provider_url", firstString(alert, root, "dashboardURL", "panelURL", "generatorURL", "ruleUrl"))
	put(meta, "error_code", firstNonEmpty(stringMapField(labels, "error_code"), getString(root, "error_code")))
	ts := timeField(alert, "startsAt", now)
	reason := firstNonEmpty(stringMapField(annotations, "summary"), getString(root, "title"), getString(root, "ruleName"), stringMapField(labels, "alertname"))
	return finalize("grafana", firstNonEmpty(stringMapField(labels, "service"), getString(root, "service")), firstNonEmpty(stringMapField(labels, "env"), getString(root, "env")), severityFromString(firstNonEmpty(stringMapField(labels, "severity"), getString(root, "state"))), reason, stringMapField(annotations, "description"), ts, meta)
}

func normalizePagerDuty(root map[string]json.RawMessage, now time.Time) (*signals.Signal, error) {
	msg := firstObject(root, "messages")
	event := objectField(msg, "event")
	data := objectField(event, "data")
	if len(data) == 0 {
		data = objectField(root, "incident")
	}
	serviceObj := objectField(data, "service")
	meta := map[string]any{"raw_source": "pagerduty"}
	put(meta, "alert_id", firstNonEmpty(stringMapField(data, "id"), stringMapField(event, "id")))
	put(meta, "provider_url", stringMapField(data, "html_url"))
	put(meta, "error_code", stringMapField(data, "error_code"))
	put(meta, "incident_id", stringMapField(data, "incident_id"))
	ts := timeField(data, "created_at", now)
	reason := firstNonEmpty(stringMapField(data, "title"), stringMapField(data, "summary"), stringMapField(event, "event_type"))
	return finalize("pagerduty", firstNonEmpty(stringMapField(data, "service"), stringMapField(serviceObj, "summary")), stringMapField(data, "env"), severityFromString(firstNonEmpty(stringMapField(data, "urgency"), stringMapField(data, "severity"))), reason, stringMapField(data, "description"), ts, meta)
}

func finalize(source, service, env string, severity signals.Severity, reason, message string, ts time.Time, meta map[string]any) (*signals.Signal, error) {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(env) == "" || strings.TrimSpace(reason) == "" {
		return nil, &NormalizeError{Status: http.StatusBadRequest, Code: CodeMissingFields, Message: "missing required fields", Detail: "service, env, and reason are required"}
	}
	if severity == "" {
		severity = signals.SeverityWarning
	}
	return &signals.Signal{
		Type:      signals.TypeAlert,
		Source:    source,
		Service:   service,
		Env:       env,
		Severity:  severity,
		Reason:    reason,
		Message:   message,
		Metadata:  meta,
		Timestamp: ts.UTC(),
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message, detail string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "detail": detail}})
}

func has(root map[string]json.RawMessage, key string) bool {
	_, ok := root[key]
	return ok
}

func baseMeta(root map[string]json.RawMessage, source string) map[string]any {
	meta := map[string]any{"raw_source": source}
	for _, key := range []string{"alert_id", "error_code", "trace_id", "incident_id", "provider_url", "fingerprint"} {
		put(meta, key, getString(root, key))
	}
	return meta
}

func put(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func metaString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func getString(root map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(root[key], &s)
	return strings.TrimSpace(s)
}

func getSeverity(root map[string]json.RawMessage, key string) signals.Severity {
	return severityFromString(getString(root, key))
}

func severityFromString(s string) signals.Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "error", "high", "triggered":
		return signals.SeverityCritical
	case "info", "resolved", "ok":
		return signals.SeverityInfo
	case "warning", "warn", "":
		return signals.SeverityWarning
	default:
		return signals.SeverityWarning
	}
}

func getTime(root map[string]json.RawMessage, key string, fallback time.Time) time.Time {
	if t := timeField(root, key, time.Time{}); !t.IsZero() {
		return t
	}
	return fallback
}

func timeField(root map[string]json.RawMessage, key string, fallback time.Time) time.Time {
	raw, ok := root[key]
	if !ok {
		return fallback
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return fallback
}

func firstObject(root map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(root[key], &arr); err == nil && len(arr) > 0 {
		return arr[0]
	}
	return nil
}

func objectField(root map[string]json.RawMessage, key string) map[string]json.RawMessage {
	if root == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(root[key], &obj); err != nil {
		return nil
	}
	return obj
}

func stringMapField(root map[string]json.RawMessage, key string) string {
	if root == nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(root[key], &s)
	return strings.TrimSpace(s)
}

func firstString(primary, secondary map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if s := stringMapField(primary, key); s != "" {
			return s
		}
		if s := stringMapField(secondary, key); s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
