package coldstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
)

type IncidentStore struct {
	db *SQLiteStore
}

func NewIncidentStore(db *SQLiteStore) *IncidentStore {
	return &IncidentStore{db: db}
}

func (s *IncidentStore) Upsert(ctx context.Context, inc incidents.Incident) error {
	if err := upsertIncident(ctx, s.db.writer, inc); err != nil {
		return fmt.Errorf("coldstore upsert incident: %w", err)
	}
	return nil
}

func (s *IncidentStore) ReplaceNonResolved(ctx context.Context, rows []incidents.Incident) error {
	tx, err := s.db.writer.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("coldstore replace incidents begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `DELETE FROM incidents WHERE status != ?`, string(incidents.StatusResolved)); err != nil {
		return fmt.Errorf("coldstore replace incidents delete: %w", err)
	}
	for _, inc := range rows {
		if err := upsertIncident(ctx, tx, inc); err != nil {
			return fmt.Errorf("coldstore replace incident %s: %w", inc.IncidentID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("coldstore replace incidents commit: %w", err)
	}
	committed = true
	return nil
}

type incidentExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func upsertIncident(ctx context.Context, execer incidentExecer, inc incidents.Incident) error {
	topServices, err := jsonText(inc.TopServices)
	if err != nil {
		return fmt.Errorf("coldstore incident top services: %w", err)
	}
	samples, err := jsonText(inc.SampleTraces)
	if err != nil {
		return fmt.Errorf("coldstore incident samples: %w", err)
	}
	evidence, err := jsonText(inc.Evidence)
	if err != nil {
		return fmt.Errorf("coldstore incident evidence: %w", err)
	}
	nextChecks, err := jsonText(inc.NextChecks)
	if err != nil {
		return fmt.Errorf("coldstore incident next checks: %w", err)
	}
	warnings, err := jsonText(inc.InstrumentationWarnings)
	if err != nil {
		return fmt.Errorf("coldstore incident warnings: %w", err)
	}
	propagation, err := jsonText(inc.Propagation)
	if err != nil {
		return fmt.Errorf("coldstore incident propagation: %w", err)
	}
	blast, err := jsonText(inc.Blast)
	if err != nil {
		return fmt.Errorf("coldstore incident blast: %w", err)
	}
	alerts, err := jsonText(inc.Alerts)
	if err != nil {
		return fmt.Errorf("coldstore incident alerts: %w", err)
	}
	_, err = execer.ExecContext(ctx, `
		INSERT INTO incidents (
			incident_id, env, service, error_service, error_step, error_code,
			status, cause, confidence, severity, started_at, updated_at, last_seen_at,
			recovering_at, resolved_at, affected_requests, affected_users, affected_services,
			top_services, sample_traces, evidence, next_checks, instrumentation_warnings,
			lift, baseline_count, current_count, propagation_json, blast_json, alert_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(incident_id) DO UPDATE SET
			status = excluded.status,
			cause = excluded.cause,
			confidence = excluded.confidence,
			severity = excluded.severity,
			updated_at = excluded.updated_at,
			last_seen_at = excluded.last_seen_at,
			recovering_at = excluded.recovering_at,
			resolved_at = excluded.resolved_at,
			affected_requests = excluded.affected_requests,
			affected_users = excluded.affected_users,
			affected_services = excluded.affected_services,
			top_services = excluded.top_services,
			sample_traces = excluded.sample_traces,
			evidence = excluded.evidence,
			next_checks = excluded.next_checks,
			instrumentation_warnings = excluded.instrumentation_warnings,
			lift = excluded.lift,
			baseline_count = excluded.baseline_count,
			current_count = excluded.current_count,
			propagation_json = excluded.propagation_json,
			blast_json = excluded.blast_json,
			alert_json = excluded.alert_json`,
		inc.IncidentID, inc.Env, inc.Service, inc.ErrorFamily.Service, inc.ErrorFamily.Step, inc.ErrorFamily.ErrorCode,
		string(inc.Status), string(inc.Cause), string(inc.Confidence), inc.Severity,
		formatTime(inc.StartedAt), formatTime(inc.UpdatedAt), formatTime(inc.LastSeenAt),
		nullableTime(inc.RecoveringAt), nullableTime(inc.ResolvedAt),
		inc.AffectedRequests, nullableInt(inc.AffectedUsers), inc.AffectedServices,
		topServices, samples, evidence, nextChecks, warnings, inc.Lift, inc.BaselineCount, inc.CurrentCount,
		propagation, blast, alerts,
	)
	if err != nil {
		return err
	}
	return nil
}

func (s *IncidentStore) Get(ctx context.Context, id string) (incidents.Incident, error) {
	row := s.db.reader.QueryRowContext(ctx, incidentSelectSQL()+` WHERE incident_id = ?`, id)
	inc, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return incidents.Incident{}, incidents.ErrNotFound
	}
	return inc, err
}

func (s *IncidentStore) ListActive(ctx context.Context) ([]incidents.Incident, error) {
	rows, err := s.db.reader.QueryContext(ctx, incidentSelectSQL()+` WHERE status != ? ORDER BY severity DESC, started_at DESC, incident_id ASC`, string(incidents.StatusResolved))
	if err != nil {
		return nil, fmt.Errorf("coldstore list active incidents: %w", err)
	}
	defer rows.Close()
	var out []incidents.Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *IncidentStore) PruneResolvedOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.writer.ExecContext(ctx, `DELETE FROM incidents WHERE status = ? AND resolved_at IS NOT NULL AND resolved_at < ?`, string(incidents.StatusResolved), formatTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("coldstore prune incidents: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("coldstore prune incidents rows affected: %w", err)
	}
	return int(n), nil
}

func incidentSelectSQL() string {
	return `SELECT incident_id, env, service, error_service, error_step, error_code,
		status, cause, confidence, severity, started_at, updated_at, last_seen_at,
		COALESCE(recovering_at, ''), COALESCE(resolved_at, ''),
		affected_requests, affected_users, affected_services,
		COALESCE(top_services, ''), COALESCE(sample_traces, ''), COALESCE(evidence, ''),
		COALESCE(next_checks, ''), COALESCE(instrumentation_warnings, ''),
		lift, baseline_count, current_count,
		COALESCE(propagation_json, ''), COALESCE(blast_json, ''), COALESCE(alert_json, '')
		FROM incidents`
}

func scanIncident(row interface{ Scan(dest ...any) error }) (incidents.Incident, error) {
	var inc incidents.Incident
	var status, cause, confidence string
	var startedAt, updatedAt, lastSeenAt, recoveringAt, resolvedAt string
	var affectedUsers sql.NullInt64
	var topServices, samples, evidence, nextChecks, warnings string
	var propagationJSON, blastJSON, alertJSON string
	err := row.Scan(
		&inc.IncidentID, &inc.Env, &inc.Service, &inc.ErrorFamily.Service, &inc.ErrorFamily.Step, &inc.ErrorFamily.ErrorCode,
		&status, &cause, &confidence, &inc.Severity, &startedAt, &updatedAt, &lastSeenAt,
		&recoveringAt, &resolvedAt, &inc.AffectedRequests, &affectedUsers, &inc.AffectedServices,
		&topServices, &samples, &evidence, &nextChecks, &warnings, &inc.Lift, &inc.BaselineCount, &inc.CurrentCount,
		&propagationJSON, &blastJSON, &alertJSON,
	)
	if err != nil {
		return incidents.Incident{}, err
	}
	inc.Status = incidents.Status(status)
	inc.Cause = incidents.Cause(cause)
	inc.Confidence = incidents.Confidence(confidence)
	var parseErr error
	if inc.StartedAt, parseErr = time.Parse(tsFormat, startedAt); parseErr != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident started_at: %w", parseErr)
	}
	if inc.UpdatedAt, parseErr = time.Parse(tsFormat, updatedAt); parseErr != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident updated_at: %w", parseErr)
	}
	if inc.LastSeenAt, parseErr = time.Parse(tsFormat, lastSeenAt); parseErr != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident last_seen_at: %w", parseErr)
	}
	if recoveringAt != "" {
		t, err := time.Parse(tsFormat, recoveringAt)
		if err != nil {
			return incidents.Incident{}, fmt.Errorf("coldstore incident recovering_at: %w", err)
		}
		inc.RecoveringAt = &t
	}
	if resolvedAt != "" {
		t, err := time.Parse(tsFormat, resolvedAt)
		if err != nil {
			return incidents.Incident{}, fmt.Errorf("coldstore incident resolved_at: %w", err)
		}
		inc.ResolvedAt = &t
	}
	if affectedUsers.Valid {
		v := int(affectedUsers.Int64)
		inc.AffectedUsers = &v
	}
	if err := parseJSONText(topServices, &inc.TopServices); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident top services: %w", err)
	}
	if err := parseJSONText(samples, &inc.SampleTraces); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident samples: %w", err)
	}
	if err := parseJSONText(evidence, &inc.Evidence); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident evidence: %w", err)
	}
	if err := parseJSONText(nextChecks, &inc.NextChecks); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident next checks: %w", err)
	}
	if err := parseJSONText(warnings, &inc.InstrumentationWarnings); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident warnings: %w", err)
	}
	if err := parseJSONText(propagationJSON, &inc.Propagation); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident propagation: %w", err)
	}
	if err := parseJSONText(blastJSON, &inc.Blast); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident blast: %w", err)
	}
	if err := parseJSONText(alertJSON, &inc.Alerts); err != nil {
		return incidents.Incident{}, fmt.Errorf("coldstore incident alerts: %w", err)
	}
	return inc, nil
}

func jsonText(v any) (sql.NullString, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	if string(raw) == "null" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: string(raw), Valid: true}, nil
}

func parseJSONText(raw string, out any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(tsFormat)
}

func nullableTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}

func nullableInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

var _ incidents.Store = (*IncidentStore)(nil)
