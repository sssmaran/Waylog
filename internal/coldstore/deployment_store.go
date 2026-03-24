package coldstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrEnvConflict is returned when an upsert would change the env of an existing deployment.
var ErrEnvConflict = errors.New("coldstore: deployment env conflict")

// Deployment represents a versioned deployment of a service in a specific environment.
type Deployment struct {
	ID        string
	Service   string
	Version   string
	Env       string
	FirstSeen time.Time
	LastSeen  time.Time
	Metadata  map[string]string
}

// ServiceErrorRate holds aggregate success/failure counts for a service in a time window.
type ServiceErrorRate struct {
	Total    int
	Failures int
}

// UpsertDeployment inserts or updates a deployment record.
// Uses BEGIN IMMEDIATE to detect env conflicts atomically.
// Empty version does not overwrite an existing non-empty version (NULLIF).
// first_seen takes the MIN, last_seen takes the MAX of existing and new values.
func (s *SQLiteStore) UpsertDeployment(ctx context.Context, d Deployment) error {
	metaJSON, err := marshalMeta(d.Metadata)
	if err != nil {
		return fmt.Errorf("coldstore: marshal metadata: %w", err)
	}

	firstSeenStr := d.FirstSeen.UTC().Format(tsFormat)
	lastSeenStr := d.LastSeen.UTC().Format(tsFormat)

	tx, err := s.writer.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("coldstore: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Check for env conflict on existing record.
	var existingEnv sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT env FROM deployments WHERE id = ?`, d.ID).Scan(&existingEnv)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("coldstore: check existing deployment: %w", err)
	}
	if existingEnv.Valid && existingEnv.String != d.Env {
		return ErrEnvConflict
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO deployments (id, service, version, env, first_seen, last_seen, metadata)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			version    = COALESCE(NULLIF(excluded.version, ''), version),
			first_seen = MIN(first_seen, excluded.first_seen),
			last_seen  = MAX(last_seen, excluded.last_seen),
			metadata   = COALESCE(excluded.metadata, metadata)
	`,
		d.ID, d.Service, d.Version, d.Env,
		firstSeenStr, lastSeenStr, metaJSON,
	)
	if err != nil {
		return fmt.Errorf("coldstore: upsert deployment: %w", err)
	}

	return tx.Commit()
}

// DeploymentsInWindow returns deployments whose first_seen falls within [start, end].
// If serviceFilter is non-empty, only deployments for that service are returned.
// Malformed metadata is tolerated: a warning is logged and an empty map is used.
func (s *SQLiteStore) DeploymentsInWindow(ctx context.Context, start, end time.Time, serviceFilter string) ([]Deployment, error) {
	var args []any
	query := `SELECT id, service, COALESCE(version,''), env, first_seen, last_seen, COALESCE(metadata,'')
		FROM deployments
		WHERE first_seen >= ? AND first_seen <= ?`
	args = append(args, start.UTC().Format(tsFormat), end.UTC().Format(tsFormat))

	if serviceFilter != "" {
		query += ` AND service = ?`
		args = append(args, serviceFilter)
	}
	query += ` ORDER BY first_seen DESC`

	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("coldstore: deployments in window: %w", err)
	}
	defer rows.Close()

	var results []Deployment
	for rows.Next() {
		var dep Deployment
		var firstStr, lastStr, metaStr string
		if err := rows.Scan(&dep.ID, &dep.Service, &dep.Version, &dep.Env, &firstStr, &lastStr, &metaStr); err != nil {
			return nil, fmt.Errorf("coldstore: scan deployment: %w", err)
		}
		dep.FirstSeen, err = time.Parse(tsFormat, firstStr)
		if err != nil {
			return nil, fmt.Errorf("coldstore: bad first_seen %q: %w", firstStr, err)
		}
		dep.LastSeen, err = time.Parse(tsFormat, lastStr)
		if err != nil {
			return nil, fmt.Errorf("coldstore: bad last_seen %q: %w", lastStr, err)
		}
		meta, parseErr := unmarshalMeta(metaStr)
		if parseErr != nil {
			slog.Warn("coldstore: malformed metadata, using empty map", "id", dep.ID, "err", parseErr)
			meta = map[string]string{}
		}
		dep.Metadata = meta
		results = append(results, dep)
	}
	return results, rows.Err()
}

// DeploymentByID retrieves a single deployment by its ID.
// Returns nil, nil if no deployment with that ID exists.
// Malformed metadata returns an error (strict).
func (s *SQLiteStore) DeploymentByID(ctx context.Context, id string) (*Deployment, error) {
	var dep Deployment
	var firstStr, lastStr, metaStr string

	err := s.reader.QueryRowContext(ctx, `
		SELECT id, service, COALESCE(version,''), env, first_seen, last_seen, COALESCE(metadata,'')
		FROM deployments WHERE id = ?`, id,
	).Scan(&dep.ID, &dep.Service, &dep.Version, &dep.Env, &firstStr, &lastStr, &metaStr)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("coldstore: deployment by id: %w", err)
	}

	dep.FirstSeen, err = time.Parse(tsFormat, firstStr)
	if err != nil {
		return nil, fmt.Errorf("coldstore: bad first_seen %q: %w", firstStr, err)
	}
	dep.LastSeen, err = time.Parse(tsFormat, lastStr)
	if err != nil {
		return nil, fmt.Errorf("coldstore: bad last_seen %q: %w", lastStr, err)
	}

	meta, parseErr := unmarshalMeta(metaStr)
	if parseErr != nil {
		return nil, fmt.Errorf("coldstore: malformed metadata for deployment %q: %w", id, parseErr)
	}
	dep.Metadata = meta
	return &dep, nil
}

// ServiceErrorRateInWindow returns the total and failure counts for a service
// in the given time window by querying the events table.
func (s *SQLiteStore) ServiceErrorRateInWindow(ctx context.Context, service string, start, end time.Time) (ServiceErrorRate, error) {
	var rate ServiceErrorRate
	err := s.reader.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0)
		FROM events
		WHERE service = ? AND timestamp >= ? AND timestamp <= ?`,
		service,
		start.UTC().Format(tsFormat),
		end.UTC().Format(tsFormat),
	).Scan(&rate.Total, &rate.Failures)
	if err != nil {
		return ServiceErrorRate{}, fmt.Errorf("coldstore: service error rate: %w", err)
	}
	return rate, nil
}

// WriterForTest returns the writer DB handle. Test-only.
func (s *SQLiteStore) WriterForTest() *sql.DB { return s.writer }

// marshalMeta serializes a metadata map to a JSON string.
// Returns nil if the map is nil or empty.
func marshalMeta(m map[string]string) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// unmarshalMeta deserializes a JSON string into a metadata map.
// An empty string returns an empty map without error.
func unmarshalMeta(s string) (map[string]string, error) {
	if s == "" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}
