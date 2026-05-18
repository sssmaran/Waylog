package microdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

const (
	proofWindow       = "15m"
	proofPollDelay    = 750 * time.Millisecond
	proofPollAttempts = 24
)

type ProofSummary struct {
	AlertID    string            `json:"alert_id"`
	IncidentID string            `json:"incident_id"`
	ReportHash string            `json:"report_hash"`
	Hashes     map[string]string `json:"hashes"`
	Burst      BurstSummary      `json:"burst"`
	Evidence   ProofEvidence     `json:"evidence"`
	Reports    ProofReports      `json:"reports"`
	Scorecard  ProofScorecard    `json:"scorecard"`
}

type ProofEvidence struct {
	TraceID          string `json:"trace_id"`
	AlertLinked      bool   `json:"alert_linked"`
	DependencySignal bool   `json:"dependency_signal"`
	NextChecks       bool   `json:"next_checks"`
}

type ProofReports struct {
	Markdown  string          `json:"markdown"`
	Slack     json.RawMessage `json:"slack"`
	PagerDuty string          `json:"pagerduty"`
}

type ProofScorecard struct {
	RootCauseAccuracy               bool   `json:"root_cause_accuracy"`
	CauseClassificationDependency   bool   `json:"cause_classification_dependency"`
	ReportHashStable                bool   `json:"report_hash_stable"`
	PropagatedErrorInflationAvoided int    `json:"propagated_error_inflation_avoided"`
	TriageLatencyMS                 int64  `json:"triage_latency_ms"`
	Scenario                        string `json:"scenario"`
	RootCauseCount                  int    `json:"root_cause_count"`
	NaivePropagatedCount            int    `json:"naive_propagated_count"`
}

type planResult struct {
	Steps []struct {
		Result json.RawMessage `json:"result"`
	} `json:"steps"`
}

func (h *GatewayHandler) ServeProof(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.ingestURL == "" {
		http.Error(w, "INGEST_URL is not configured for the demo proof", http.StatusServiceUnavailable)
		return
	}

	var req BurstRequest
	if r.Body != nil {
		defer r.Body.Close()
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	result, err := h.runProof(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *GatewayHandler) runProof(ctx context.Context, req BurstRequest) (ProofSummary, error) {
	alertID := fmt.Sprintf("alert_demo_proof_pmt_502_%d", time.Now().Unix())
	if err := h.postProofAlert(ctx, alertID); err != nil {
		return ProofSummary{}, err
	}

	signals := []SignalResult(nil)
	if h.signals != nil {
		signals = h.signals.PostDemoSignals(ctx)
	}
	burst := runBurst(ctx, h.purchase, req)
	burst.Signals = signals
	answerStart := time.Now()

	errorsResp, err := h.pollErrors(ctx)
	if err != nil {
		return ProofSummary{}, err
	}
	inc, incidentsResp, err := h.pollIncident(ctx)
	if err != nil {
		return ProofSummary{}, err
	}

	triageA, err := h.getTriage(ctx, inc.IncidentID)
	if err != nil {
		return ProofSummary{}, err
	}
	triageB, err := h.getTriage(ctx, inc.IncidentID)
	if err != nil {
		return ProofSummary{}, err
	}
	answerEnd := time.Now()

	toolReport, err := h.postToolTriage(ctx, inc.IncidentID)
	if err != nil {
		return ProofSummary{}, err
	}
	planReport, err := h.postPlanTriage(ctx, inc.IncidentID)
	if err != nil {
		return ProofSummary{}, err
	}
	hashes := map[string]string{
		"read":   triageA.ReportHash,
		"repeat": triageB.ReportHash,
		"tool":   toolReport.ReportHash,
		"plan":   planReport.ReportHash,
	}
	hashStable := triageA.ReportHash != "" &&
		triageA.ReportHash == triageB.ReportHash &&
		triageA.ReportHash == toolReport.ReportHash &&
		triageA.ReportHash == planReport.ReportHash

	blast, err := h.getBlast(ctx)
	if err != nil {
		return ProofSummary{}, err
	}
	reports, err := h.getReports(ctx, inc.IncidentID)
	if err != nil {
		return ProofSummary{}, err
	}

	rootCount := paymentErrorCount(errorsResp)
	naive := rootCount * blast.AffectedServices
	return ProofSummary{
		AlertID:    alertID,
		IncidentID: inc.IncidentID,
		ReportHash: triageA.ReportHash,
		Hashes:     hashes,
		Burst:      burst,
		Evidence: ProofEvidence{
			TraceID:          firstTraceID(triageA),
			AlertLinked:      hasAlertID(triageA, alertID),
			DependencySignal: hasSignalType(triageA, "dependency"),
			NextChecks:       len(triageA.NextChecks) > 0,
		},
		Reports: reports,
		Scorecard: ProofScorecard{
			RootCauseAccuracy:               triageRootCauseAccurate(triageA),
			CauseClassificationDependency:   incidentCauseIsDependency(incidentsResp, inc.IncidentID),
			ReportHashStable:                hashStable,
			PropagatedErrorInflationAvoided: naive - rootCount,
			TriageLatencyMS:                 answerEnd.Sub(answerStart).Milliseconds(),
			Scenario:                        "warm-demo",
			RootCauseCount:                  rootCount,
			NaivePropagatedCount:            naive,
		},
	}, nil
}

func (h *GatewayHandler) postProofAlert(ctx context.Context, alertID string) error {
	body := map[string]any{
		"source":     "waylog",
		"alert_id":   alertID,
		"service":    "checkout",
		"env":        "demo",
		"severity":   "critical",
		"reason":     "PMT_502 spike",
		"message":    "browser demo alert for checkout payment failures",
		"error_code": "PMT_502",
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}
	status, _, err := h.doJSON(ctx, http.MethodPost, "/v1/alerts", h.writeKey, body, nil)
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("alert webhook failed: HTTP %d", status)
	}
	return nil
}

func (h *GatewayHandler) pollErrors(ctx context.Context) (apiv2.ErrorsResponse, error) {
	var last apiv2.ErrorsResponse
	for i := 0; i < proofPollAttempts; i++ {
		q := url.Values{"window": {proofWindow}, "limit": {"10"}}
		status, _, err := h.doJSON(ctx, http.MethodGet, "/v1/errors?"+q.Encode(), h.readKey, nil, &last)
		if err == nil && status == http.StatusOK && paymentErrorCount(last) > 0 {
			return last, nil
		}
		if err != nil {
			return apiv2.ErrorsResponse{}, err
		}
		sleepOrDone(ctx, proofPollDelay)
	}
	return apiv2.ErrorsResponse{}, fmt.Errorf("payment_502 error family did not appear")
}

func (h *GatewayHandler) pollIncident(ctx context.Context) (apiv2.Incident, apiv2.IncidentListResponse, error) {
	var last apiv2.IncidentListResponse
	for i := 0; i < proofPollAttempts; i++ {
		status, _, err := h.doJSON(ctx, http.MethodGet, "/v1/incidents/active", h.readKey, nil, &last)
		if err != nil {
			return apiv2.Incident{}, apiv2.IncidentListResponse{}, err
		}
		if status == http.StatusOK {
			for _, inc := range last.Incidents {
				if isPaymentFamily(inc.ErrorFamily) && inc.Cause == "dependency" && inc.Status == "active" {
					return inc, last, nil
				}
			}
		}
		sleepOrDone(ctx, proofPollDelay)
	}
	return apiv2.Incident{}, apiv2.IncidentListResponse{}, fmt.Errorf("dependency incident did not appear")
}

func (h *GatewayHandler) getTriage(ctx context.Context, incidentID string) (*pkgtriage.Report, error) {
	var rep pkgtriage.Report
	status, _, err := h.doJSON(ctx, http.MethodGet, "/v1/triage/"+url.PathEscape(incidentID)+"?snapshot=true", h.readKey, nil, &rep)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("triage read failed: HTTP %d", status)
	}
	return &rep, nil
}

func (h *GatewayHandler) postToolTriage(ctx context.Context, incidentID string) (*pkgtriage.Report, error) {
	var rep pkgtriage.Report
	status, _, err := h.doJSON(ctx, http.MethodPost, "/v1/tools/triage_incident", h.agentKey, map[string]any{"incident_id": incidentID, "snapshot": true}, &rep)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("triage tool failed: HTTP %d", status)
	}
	return &rep, nil
}

func (h *GatewayHandler) postPlanTriage(ctx context.Context, incidentID string) (*pkgtriage.Report, error) {
	var plan planResult
	status, _, err := h.doJSON(ctx, http.MethodPost, "/v1/plans/execute", h.agentKey, map[string]any{
		"template": "triage",
		"params":   map[string]any{"incident_id": incidentID, "snapshot": true},
	}, &plan)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || len(plan.Steps) == 0 {
		return nil, fmt.Errorf("triage plan failed: HTTP %d", status)
	}
	var rep pkgtriage.Report
	if err := json.Unmarshal(plan.Steps[0].Result, &rep); err != nil {
		return nil, fmt.Errorf("triage plan result decode: %w", err)
	}
	return &rep, nil
}

func (h *GatewayHandler) getBlast(ctx context.Context) (apiv2.BlastRadiusResponse, error) {
	q := url.Values{"window": {proofWindow}, "error_family": {"checkout:payment.charge:PMT_502"}}
	var blast apiv2.BlastRadiusResponse
	status, _, err := h.doJSON(ctx, http.MethodGet, "/v1/blast_radius?"+q.Encode(), h.readKey, nil, &blast)
	if err != nil {
		return apiv2.BlastRadiusResponse{}, err
	}
	if status != http.StatusOK {
		return apiv2.BlastRadiusResponse{}, fmt.Errorf("blast failed: HTTP %d", status)
	}
	return blast, nil
}

func (h *GatewayHandler) getReports(ctx context.Context, incidentID string) (ProofReports, error) {
	var out ProofReports
	for _, format := range []string{"markdown", "slack", "pagerduty"} {
		path := "/v1/triage/" + url.PathEscape(incidentID) + "/report?format=" + format + "&snapshot=true"
		status, raw, err := h.doJSON(ctx, http.MethodGet, path, h.readKey, nil, nil)
		if err != nil {
			return ProofReports{}, err
		}
		if status != http.StatusOK {
			return ProofReports{}, fmt.Errorf("%s report failed: HTTP %d", format, status)
		}
		switch format {
		case "markdown":
			out.Markdown = string(raw)
		case "slack":
			out.Slack = append(json.RawMessage(nil), raw...)
		case "pagerduty":
			out.PagerDuty = string(raw)
		}
	}
	return out, nil
}

func (h *GatewayHandler) doJSON(ctx context.Context, method, path, key string, body any, out any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.ingestURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := h.proofClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, raw, err
		}
	}
	return resp.StatusCode, raw, nil
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func paymentErrorCount(resp apiv2.ErrorsResponse) int {
	for _, row := range resp.Rows {
		if isPaymentFamily(row.ErrorFamily) {
			return row.Count
		}
	}
	return 0
}

func isPaymentFamily(f apiv2.ErrorFamily) bool {
	return f.Service == "checkout" && f.Step == "payment.charge" && f.ErrorCode == "PMT_502"
}

func firstTraceID(rep *pkgtriage.Report) string {
	if rep == nil || len(rep.SampleTraces) == 0 {
		return ""
	}
	return rep.SampleTraces[0].TraceID
}

func hasAlertID(rep *pkgtriage.Report, alertID string) bool {
	if rep == nil {
		return false
	}
	for _, alert := range rep.Alerts {
		if alert.AlertID == alertID && alert.SignalID != "" {
			return true
		}
	}
	return false
}

func hasSignalType(rep *pkgtriage.Report, typ string) bool {
	if rep == nil {
		return false
	}
	for _, sig := range rep.Signals {
		if sig.ID != "" && sig.Type == typ {
			return true
		}
	}
	return false
}

func triageRootCauseAccurate(rep *pkgtriage.Report) bool {
	if rep == nil {
		return false
	}
	for _, family := range rep.BlastSnapshot.TopErrorFamilies {
		if family.Service == "checkout" && family.Step == "payment.charge" && family.ErrorCode == "PMT_502" {
			return true
		}
	}
	return false
}

func incidentCauseIsDependency(resp apiv2.IncidentListResponse, incidentID string) bool {
	for _, inc := range resp.Incidents {
		if inc.IncidentID == incidentID {
			return inc.Cause == "dependency"
		}
	}
	return false
}
