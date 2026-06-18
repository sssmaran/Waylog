package firstrun

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type reportPoll struct {
	IngestURL string
	ReadKey   string
	Timeout   time.Duration
	Interval  time.Duration
}

type reportResult struct {
	IncidentID string
	ReportHash string
	Markdown   string
}

func getWithKey(url, key string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := burstHTTPClient.Do(req) // reuse the 5s-timeout client from burst.go
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode, nil
}

func waitForReport(p reportPoll) (reportResult, error) {
	if p.Interval <= 0 {
		p.Interval = time.Second
	}
	deadline := time.Now().Add(p.Timeout)
	var incidentID string
	for time.Now().Before(deadline) {
		body, code, err := getWithKey(p.IngestURL+"/v1/incidents/active", p.ReadKey)
		if err == nil && code == http.StatusOK {
			var active struct {
				Incidents []struct {
					IncidentID string `json:"incident_id"`
				} `json:"incidents"`
			}
			if json.Unmarshal(body, &active) == nil && len(active.Incidents) > 0 {
				incidentID = active.Incidents[0].IncidentID
				break
			}
		}
		time.Sleep(p.Interval)
	}
	if incidentID == "" {
		return reportResult{}, fmt.Errorf("no incident opened within %s", p.Timeout)
	}

	// /v1/triage/{id} returns pkgtriage.Report directly; report_hash is a top-level field.
	// snapshot=true must match the markdown fetch below so the printed hash and
	// the printed report describe the same frozen state across an engine tick.
	jsonBody, _, err := getWithKey(p.IngestURL+"/v1/triage/"+incidentID+"?snapshot=true", p.ReadKey)
	if err != nil {
		return reportResult{}, fmt.Errorf("fetch triage json: %w", err)
	}
	var triage struct {
		ReportHash string `json:"report_hash"`
	}
	_ = json.Unmarshal(jsonBody, &triage)

	mdBody, _, err := getWithKey(p.IngestURL+"/v1/triage/"+incidentID+"/report?format=markdown&snapshot=true", p.ReadKey)
	if err != nil {
		return reportResult{}, fmt.Errorf("fetch triage report: %w", err)
	}
	return reportResult{
		IncidentID: incidentID,
		ReportHash: triage.ReportHash,
		Markdown:   string(mdBody),
	}, nil
}
