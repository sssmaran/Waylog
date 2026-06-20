// Package triage exposes the TriageReport schema. Experimental: report shape may change until triage.v2.
package triage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

type Report struct {
	SchemaVersion string          `json:"schema_version"`
	IncidentRef   IncidentRef     `json:"incident_ref"`
	BlastSnapshot BlastSnapshot   `json:"blast_snapshot"`
	FirstFailure  json.RawMessage `json:"first_failure,omitempty"`
	SuspectChange *SuspectChange  `json:"suspect_change,omitempty"`
	SampleTraces  []TraceSample   `json:"sample_traces,omitempty"`
	Signals       []SignalRef     `json:"signals,omitempty"`
	Alerts        []AlertRef      `json:"alerts,omitempty"`
	Runtime       []RuntimeRef    `json:"runtime,omitempty"`
	NextChecks    []NextCheck     `json:"next_checks,omitempty"`
	Confidence    Confidence      `json:"confidence"`
	GeneratedAt   string          `json:"generated_at"`
	PlanRunID     string          `json:"plan_run_id,omitempty"`
	ReportHash    string          `json:"report_hash"`
	// EvidenceFingerprint identifies the evidence set grounding this report;
	// unlike ReportHash it is stable across engine ticks until evidence is
	// attached or removed (ADR 0002). omitempty keeps pre-fingerprint
	// report_hash values unchanged.
	EvidenceFingerprint string `json:"evidence_fingerprint,omitempty"`
}

type IncidentRef struct {
	ID     string `json:"id"`
	Window string `json:"window"`
}

type BlastSnapshot struct {
	Requests         int           `json:"requests"`
	Users            int           `json:"users"`
	Services         int           `json:"services"`
	TopErrorFamilies []ErrorFamily `json:"top_error_families"`
}

type ErrorFamily struct {
	Service   string `json:"service"`
	Step      string `json:"step"`
	ErrorCode string `json:"error_code"`
	Count     int    `json:"count"`
}

// SuspectChange is the deployment correlated to the incident by the incident
// classifier, enriched with CI-pushed provenance. The identity fields participate
// in report_hash; ErrorRateBefore/ErrorRateAfter are volatile measurements and
// are deliberately excluded from the hash (see CanonicalHash), the same treatment
// RuntimeRef gives its capture timestamp.
type SuspectChange struct {
	DeployID        string   `json:"deploy_id"`
	Service         string   `json:"service"`
	Version         string   `json:"version,omitempty"`
	CommitSHA       string   `json:"commit_sha,omitempty"`
	PRURL           string   `json:"pr_url,omitempty"`
	CommitAuthor    string   `json:"commit_author,omitempty"`
	DeployedAt      string   `json:"deployed_at,omitempty"`
	ErrorRateBefore *float64 `json:"error_rate_before,omitempty"`
	ErrorRateAfter  *float64 `json:"error_rate_after,omitempty"`
}

type TraceSample struct {
	TraceID string `json:"trace_id"`
	Summary string `json:"summary"`
}

type SignalRef struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type AlertRef struct {
	SignalID    string   `json:"signal_id"`
	AlertID     string   `json:"alert_id,omitempty"`
	Source      string   `json:"source"`
	Severity    string   `json:"severity"`
	Reason      string   `json:"reason"`
	ProviderURL string   `json:"provider_url,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// RuntimeRef is a runtime evidence row in the report — infra (k8s OOMKill,
// crashloop) or app (panic, unhandled rejection). It deliberately omits the
// capture timestamp: only stable signal fields participate in report_hash so
// the hash does not churn as fresh captures update CapturedAt.
type RuntimeRef struct {
	SignalID   string `json:"signal_id"`
	Subtype    string `json:"subtype"`
	Service    string `json:"service"`
	Source     string `json:"source"`
	Severity   string `json:"severity"`
	Reason     string `json:"reason"`
	OccurredAt string `json:"occurred_at"`
}

type NextCheck struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

const SchemaVersionV1 = "triage.v1"

func (r *Report) Validate() error {
	if r.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("triage: schema_version must be %q, got %q", SchemaVersionV1, r.SchemaVersion)
	}
	if r.IncidentRef.ID == "" {
		return fmt.Errorf("triage: incident_ref.id required")
	}
	switch r.Confidence {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
	default:
		return fmt.Errorf("triage: confidence must be low|medium|high, got %q", r.Confidence)
	}
	if r.GeneratedAt == "" {
		return fmt.Errorf("triage: generated_at required")
	}
	return nil
}

// CanonicalHash returns sha256:<hex> over the report's canonical JSON,
// excluding generated_at, plan_run_id, and report_hash itself.
// Two reports built from the same upstream state produce the same hash.
func (r *Report) CanonicalHash() (string, error) {
	clone := *r
	clone.GeneratedAt = ""
	clone.PlanRunID = ""
	clone.ReportHash = ""
	clone.EvidenceFingerprint = ""
	// SuspectChange is a pointer (shared with r); copy it and drop the volatile
	// measured rates so the hash stays stable while the after-window fills.
	if clone.SuspectChange != nil {
		sc := *clone.SuspectChange
		sc.ErrorRateBefore = nil
		sc.ErrorRateAfter = nil
		clone.SuspectChange = &sc
	}
	raw, err := json.Marshal(&clone)
	if err != nil {
		return "", fmt.Errorf("triage: canonical marshal: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalEvidenceFingerprint returns sha256:<hex> over the report's
// evidence identity set: sorted, deduplicated kind:id tuples for the
// incident, signals, alerts, runtime events, and sample traces. Volatile
// fields (counts, confidence, next checks, payloads, timestamps) are
// excluded by construction, so the fingerprint is stable across engine
// ticks until evidence is attached or removed (ADR 0002).
func (r *Report) CanonicalEvidenceFingerprint() string {
	set := map[string]struct{}{}
	add := func(kind, id string) {
		if id != "" {
			set[kind+":"+id] = struct{}{}
		}
	}
	add("incident", r.IncidentRef.ID)
	if r.SuspectChange != nil {
		add("deploy", r.SuspectChange.DeployID)
	}
	for _, s := range r.Signals {
		add("signal", s.ID)
	}
	for _, a := range r.Alerts {
		add("alert", a.SignalID)
	}
	for _, rt := range r.Runtime {
		add("runtime", rt.SignalID)
	}
	for _, t := range r.SampleTraces {
		add("trace", t.TraceID)
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}
