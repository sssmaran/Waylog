package coldstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/event"

	_ "modernc.org/sqlite"
)

// tsFormat is a fixed-width ISO 8601 format with 9 fractional digits.
// Fixed width guarantees that lexical TEXT ordering matches chronological
// ordering in SQLite (RFC3339Nano is variable-width and can misorder).
const tsFormat = "2006-01-02T15:04:05.000000000Z07:00"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the domain interface for cold storage operations.
type Store interface {
	EventSearcher
	DeploymentStore
}

// ManagedStore adds lifecycle methods to Store.
type ManagedStore interface {
	Store
	Migrate() error
	Close() error
}

// EventWriter persists a single event synchronously.
type EventWriter interface {
	WriteEvent(ctx context.Context, ev *event.WideEvent) error
}

// EventSearcher queries persisted events.
type EventSearcher interface {
	SearchEvents(f SearchFilter) (SearchPage, error)
}

// DeploymentStore manages deployment records.
type DeploymentStore interface {
	UpsertDeployment(ctx context.Context, d Deployment) error
	DeploymentByID(ctx context.Context, id string) (*Deployment, error)
	DeploymentsInWindow(ctx context.Context, start, end time.Time, serviceFilter string) ([]Deployment, error)
	ServiceErrorRateInWindow(ctx context.Context, svc string, from, to time.Time) (ServiceErrorRate, error)
	DeployErrorRateDelta(ctx context.Context, service string, firstSeen time.Time) (DeployRateDelta, error)
}

// SQLiteStore wraps a SQLite database for cold storage of events and deployments.
// It maintains separate writer (single-conn) and reader (multi-conn) handles.
type SQLiteStore struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// Open creates a new SQLiteStore backed by the SQLite database at path.
// Use ":memory:" for tests. Runs migrations automatically.
func Open(path string) (ManagedStore, error) {
	writerDSN := path
	if path != ":memory:" {
		writerDSN = path + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=ON"
	}
	writer, err := sql.Open("sqlite", writerDSN)
	if err != nil {
		return nil, fmt.Errorf("coldstore: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)

	if path == ":memory:" {
		for _, pragma := range []string{
			"PRAGMA foreign_keys = ON",
			"PRAGMA synchronous = NORMAL",
		} {
			if _, err := writer.Exec(pragma); err != nil {
				writer.Close()
				return nil, fmt.Errorf("coldstore: pragma %q: %w", pragma, err)
			}
		}
	}

	reader := writer
	if path != ":memory:" {
		readerDSN := path + "?mode=ro&_journal_mode=WAL&_busy_timeout=5000"
		reader, err = sql.Open("sqlite", readerDSN)
		if err != nil {
			writer.Close()
			return nil, fmt.Errorf("coldstore: open reader: %w", err)
		}
		reader.SetMaxOpenConns(4)
	}

	s := &SQLiteStore{writer: writer, reader: reader, path: path}

	if err := s.Migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("coldstore: migrate: %w", err)
	}

	slog.Info("coldstore opened", "path", path)
	return s, nil
}

// MigrationNames returns the embedded migration file names, sorted ascending.
// It reads the same embedded FS that Migrate applies, so callers (e.g. doctor)
// can compare applied-vs-expected without opening the database.
func MigrationNames() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("coldstore: read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Migrate runs all embedded SQL migration files idempotently. Applied
// migrations are tracked in the schema_migrations table; files already
// recorded there are skipped on subsequent calls. This lets non-idempotent
// statements (e.g. ALTER TABLE ADD COLUMN) live in a migration file and
// still satisfy the Migrate-twice contract.
func (s *SQLiteStore) Migrate() error {
	if _, err := s.writer.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	for _, entry := range entries {
		var applied string
		err := s.writer.QueryRow(`SELECT applied_at FROM schema_migrations WHERE name = ?`, entry.Name()).Scan(&applied)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if err := s.applyMigration(entry.Name(), data); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration file and records it in schema_migrations
// inside a single transaction. If either statement fails, the entire change
// is rolled back so a non-idempotent migration (e.g. ALTER TABLE ADD COLUMN)
// is never left half-applied across a crash or partial failure.
func (s *SQLiteStore) applyMigration(name string, data []byte) (err error) {
	tx, err := s.writer.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(string(data)); err != nil {
		return fmt.Errorf("exec migration %s: %w", name, err)
	}
	if _, err = tx.Exec(
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, time.Now().UTC().Format(tsFormat),
	); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// Close closes both writer and reader database handles.
func (s *SQLiteStore) Close() error {
	var firstErr error
	if s.reader != s.writer {
		if err := s.reader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.writer.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Compile-time interface satisfaction checks.
var (
	_ ManagedStore    = (*SQLiteStore)(nil)
	_ Store           = (*SQLiteStore)(nil)
	_ EventWriter     = (*SQLiteStore)(nil)
	_ EventSearcher   = (*SQLiteStore)(nil)
	_ DeploymentStore = (*SQLiteStore)(nil)
)
