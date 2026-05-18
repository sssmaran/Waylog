package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type errorsResponse struct {
	Rows []errorRow `json:"rows"`
}

type errorRow struct {
	ErrorFamily    errorFamily `json:"error_family"`
	Count          int         `json:"count"`
	AffectedTraces int         `json:"affected_traces"`
	SampleTraces   []string    `json:"sample_traces"`
}

type errorFamily struct {
	Service   string `json:"service"`
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
}

type searchResponse struct {
	Events []searchEvent `json:"events"`
}

type searchEvent struct {
	EventID string `json:"event_id"`
}

type burstSummary struct {
	Signals []signalResult `json:"signals"`
}

type signalResult struct {
	Type     string `json:"type"`
	Service  string `json:"service"`
	Reason   string `json:"reason"`
	Accepted bool   `json:"accepted"`
}

type incidentsResponse struct {
	Incidents []incident `json:"incidents"`
}

type incident struct {
	IncidentID  string      `json:"incident_id"`
	ErrorFamily errorFamily `json:"error_family"`
	Cause       string      `json:"cause"`
	Confidence  string      `json:"confidence"`
	Status      string      `json:"status"`
}

type triageReport struct {
	ReportHash    string         `json:"report_hash"`
	BlastSnapshot blastSnapshot  `json:"blast_snapshot"`
	SampleTraces  []traceSample  `json:"sample_traces"`
	Signals       []triageSignal `json:"signals"`
	Alerts        []triageAlert  `json:"alerts"`
	NextChecks    []nextCheck    `json:"next_checks"`
}

type blastSnapshot struct {
	TopErrorFamilies []errorFamily `json:"top_error_families"`
}

type traceSample struct {
	TraceID string `json:"trace_id"`
}

type triageSignal struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type triageAlert struct {
	SignalID string `json:"signal_id"`
	AlertID  string `json:"alert_id"`
}

type nextCheck struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type planResult struct {
	Steps []planStep `json:"steps"`
}

type planStep struct {
	Result json.RawMessage `json:"result"`
}

type blastResponse struct {
	AffectedServices int `json:"affected_services"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: demo-acceptance-json <has-payment-error|payment-error-count|payment-affected-traces|first-payment-trace|first-event-id|burst-signals-accepted|has-dependency-incident|incident-cause-is-dependency|first-incident-id|triage-report-hash|plan-triage-report-hash|triage-first-trace|triage-has-alert|triage-has-alert-id|triage-has-trace|triage-has-dependency-signal|triage-has-next-check|triage-root-cause-accurate|blast-affected-services> [arg]")
		os.Exit(2)
	}

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "has-payment-error":
		if !hasPaymentError(body) {
			os.Exit(1)
		}
	case "payment-error-count":
		fmt.Println(paymentErrorCount(body))
	case "payment-affected-traces":
		fmt.Println(paymentAffectedTraces(body))
	case "first-payment-trace":
		fmt.Println(firstPaymentTrace(body))
	case "first-event-id":
		fmt.Println(firstEventID(body))
	case "burst-signals-accepted":
		if !burstSignalsAccepted(body) {
			os.Exit(1)
		}
	case "has-dependency-incident":
		if !hasDependencyIncident(body) {
			os.Exit(1)
		}
	case "incident-cause-is-dependency":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "incident-cause-is-dependency requires an incident_id")
			os.Exit(2)
		}
		if !incidentCauseIsDependency(body, os.Args[2]) {
			os.Exit(1)
		}
	case "first-incident-id":
		fmt.Println(firstIncidentID(body))
	case "triage-report-hash":
		fmt.Println(triageReportHash(body))
	case "plan-triage-report-hash":
		fmt.Println(planTriageReportHash(body))
	case "triage-first-trace":
		fmt.Println(triageFirstTrace(body))
	case "triage-has-alert":
		if !triageHasAlert(body) {
			os.Exit(1)
		}
	case "triage-has-alert-id":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "triage-has-alert-id requires an alert_id")
			os.Exit(2)
		}
		if !triageHasAlertID(body, os.Args[2]) {
			os.Exit(1)
		}
	case "triage-has-trace":
		if !triageHasTrace(body) {
			os.Exit(1)
		}
	case "triage-has-dependency-signal":
		if !triageHasDependencySignal(body) {
			os.Exit(1)
		}
	case "triage-has-next-check":
		if !triageHasNextCheck(body) {
			os.Exit(1)
		}
	case "triage-root-cause-accurate":
		if !triageRootCauseAccurate(body) {
			os.Exit(1)
		}
	case "blast-affected-services":
		fmt.Println(blastAffectedServices(body))
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func hasPaymentError(body []byte) bool {
	var resp errorsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	for _, row := range resp.Rows {
		if isPayment502(row) && row.AffectedTraces > 0 {
			return true
		}
	}
	return false
}

func paymentErrorCount(body []byte) int {
	var resp errorsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	for _, row := range resp.Rows {
		if isPayment502(row) {
			return row.Count
		}
	}
	return 0
}

func paymentAffectedTraces(body []byte) int {
	var resp errorsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	for _, row := range resp.Rows {
		if isPayment502(row) {
			return row.AffectedTraces
		}
	}
	return 0
}

func firstPaymentTrace(body []byte) string {
	var resp errorsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	for _, row := range resp.Rows {
		if isPayment502(row) && len(row.SampleTraces) > 0 {
			return row.SampleTraces[0]
		}
	}
	return ""
}

func firstEventID(body []byte) string {
	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Events) == 0 {
		return ""
	}
	return resp.Events[0].EventID
}

func isPayment502(row errorRow) bool {
	return row.ErrorFamily.Service == "checkout" &&
		row.ErrorFamily.Step == "payment.charge" &&
		row.ErrorFamily.ErrorCode == "PMT_502"
}

func burstSignalsAccepted(body []byte) bool {
	var summary burstSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return false
	}
	seen := map[string]bool{}
	for _, signal := range summary.Signals {
		if signal.Accepted {
			seen[signal.Type+":"+signal.Service+":"+signal.Reason] = true
		}
	}
	return seen["deploy:checkout:demo_checkout_rollout"] &&
		seen["dependency:payment:payment_gateway_5xx"]
}

func hasDependencyIncident(body []byte) bool {
	var resp incidentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	for _, inc := range resp.Incidents {
		if isPaymentFamily(inc.ErrorFamily) &&
			inc.Cause == "dependency" &&
			(inc.Confidence == "high" || inc.Confidence == "medium") &&
			inc.Status == "active" {
			return true
		}
	}
	return false
}

func firstIncidentID(body []byte) string {
	var resp incidentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	for _, inc := range resp.Incidents {
		if isPaymentFamily(inc.ErrorFamily) {
			return inc.IncidentID
		}
	}
	return ""
}

func incidentCauseIsDependency(body []byte, incidentID string) bool {
	var resp incidentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	for _, inc := range resp.Incidents {
		if inc.IncidentID == incidentID {
			return inc.Cause == "dependency"
		}
	}
	return false
}

func isPaymentFamily(f errorFamily) bool {
	return f.Service == "checkout" &&
		f.Step == "payment.charge" &&
		f.ErrorCode == "PMT_502"
}

func triageReportHash(body []byte) string {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return ""
	}
	return rep.ReportHash
}

func planTriageReportHash(body []byte) string {
	var plan planResult
	if err := json.Unmarshal(body, &plan); err != nil || len(plan.Steps) == 0 {
		return ""
	}
	return triageReportHash(plan.Steps[0].Result)
}

func triageHasAlert(body []byte) bool {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return false
	}
	for _, alert := range rep.Alerts {
		if alert.SignalID != "" && alert.AlertID != "" {
			return true
		}
	}
	return false
}

func triageHasAlertID(body []byte, alertID string) bool {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return false
	}
	for _, alert := range rep.Alerts {
		if alert.AlertID == alertID && alert.SignalID != "" {
			return true
		}
	}
	return false
}

func triageHasTrace(body []byte) bool {
	return triageFirstTrace(body) != ""
}

func triageFirstTrace(body []byte) string {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return ""
	}
	for _, sample := range rep.SampleTraces {
		if sample.TraceID != "" {
			return sample.TraceID
		}
	}
	return ""
}

func triageHasDependencySignal(body []byte) bool {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return false
	}
	for _, sig := range rep.Signals {
		if sig.ID != "" && sig.Type == "dependency" {
			return true
		}
	}
	return false
}

func triageHasNextCheck(body []byte) bool {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return false
	}
	for _, check := range rep.NextChecks {
		if check.ID != "" && check.Prompt != "" {
			return true
		}
	}
	return false
}

func triageRootCauseAccurate(body []byte) bool {
	var rep triageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return false
	}
	for _, family := range rep.BlastSnapshot.TopErrorFamilies {
		if isPaymentFamily(family) {
			return true
		}
	}
	return false
}

func blastAffectedServices(body []byte) int {
	var resp blastResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0
	}
	return resp.AffectedServices
}
