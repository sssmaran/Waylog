package coldstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// ErrEnvConflict is returned when an upsert would change the env of an existing deployment.
var ErrEnvConflict = errors.New("coldstore: deployment env conflict")

// Deployment represents a versioned deployment of a service in a specific environment.
// CommitSHA/PRURL/CommitAuthor are optional provenance pushed by CI at deploy time;
// they are opaque, vendor-neutral strings (pr_url is a full URL).
type Deployment struct {
	ID           string
	Service      string
	Version      string
	Env          string
	FirstSeen    time.Time
	LastSeen     time.Time
	Metadata     map[string]string
	CommitSHA    string
	PRURL        string
	CommitAuthor string
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
		INSERT INTO deployments (id, service, version, env, first_seen, last_seen, metadata, commit_sha, pr_url, commit_author)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))
		ON CONFLICT(id) DO UPDATE SET
			version       = COALESCE(NULLIF(excluded.version, ''), version),
			first_seen    = MIN(first_seen, excluded.first_seen),
			last_seen     = MAX(last_seen, excluded.last_seen),
			metadata      = COALESCE(excluded.metadata, metadata),
			commit_sha    = COALESCE(excluded.commit_sha, commit_sha),
			pr_url        = COALESCE(excluded.pr_url, pr_url),
			commit_author = COALESCE(excluded.commit_author, commit_author)
	`,
		d.ID, d.Service, d.Version, d.Env,
		firstSeenStr, lastSeenStr, metaJSON,
		d.CommitSHA, d.PRURL, d.CommitAuthor,
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
	query := `SELECT id, service, COALESCE(version,''), env, first_seen, last_seen, COALESCE(metadata,''),
		COALESCE(commit_sha,''), COALESCE(pr_url,''), COALESCE(commit_author,'')
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
		if err := rows.Scan(&dep.ID, &dep.Service, &dep.Version, &dep.Env, &firstStr, &lastStr, &metaStr,
			&dep.CommitSHA, &dep.PRURL, &dep.CommitAuthor); err != nil {
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
		SELECT id, service, COALESCE(version,''), env, first_seen, last_seen, COALESCE(metadata,''),
			COALESCE(commit_sha,''), COALESCE(pr_url,''), COALESCE(commit_author,'')
		FROM deployments WHERE id = ?`, id,
	).Scan(&dep.ID, &dep.Service, &dep.Version, &dep.Env, &firstStr, &lastStr, &metaStr,
		&dep.CommitSHA, &dep.PRURL, &dep.CommitAuthor)

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

// DeployRateDelta holds the before/after error-rate comparison around a deploy.
// BeforeRate/AfterRate are failure ratios (nil when not computable). Ratio is the
// after/before multiplier, set only when both windows clear minRequests and the
// comparison is meaningful.
type DeployRateDelta struct {
	BeforeRate     *float64
	AfterRate      *float64
	BeforeRequests int
	AfterRequests  int
	Ratio          *float64
}

// DeployErrorRateDelta compares a service's failure ratio in the fixed windows
// before and after firstSeen. It is the single source for the deploy comparison
// window (5m) and the minimum-sample threshold (10) — both the deploy listing and
// triage's suspect-change call it so the thresholds live in exactly one place.
func (s *SQLiteStore) DeployErrorRateDelta(ctx context.Context, service string, firstSeen time.Time) (DeployRateDelta, error) {
	const sampleWindow = 5 * time.Minute
	const minRequests = 10

	before, berr := s.ServiceErrorRateInWindow(ctx, service, firstSeen.Add(-sampleWindow), firstSeen)
	after, aerr := s.ServiceErrorRateInWindow(ctx, service, firstSeen, firstSeen.Add(sampleWindow))

	d := DeployRateDelta{BeforeRequests: before.Total, AfterRequests: after.Total}
	if berr != nil {
		return d, berr
	}
	if aerr != nil {
		return d, aerr
	}

	bRate := float64(before.Failures) / math.Max(float64(before.Total), 1)
	aRate := float64(after.Failures) / math.Max(float64(after.Total), 1)
	d.BeforeRate = &bRate
	d.AfterRate = &aRate

	if before.Total >= minRequests && after.Total >= minRequests &&
		!(before.Failures == 0 && after.Failures == 0) && // both clean: no signal
		!(before.Failures == 0 && after.Failures > 0) { // no baseline: skip ratio
		change := aRate / math.Max(bRate, 0.001)
		d.Ratio = &change
	}
	return d, nil
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
