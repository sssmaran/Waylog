package coldstore

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"
)

// tsFormat is a fixed-width ISO 8601 format with 9 fractional digits.
// Fixed width guarantees that lexical TEXT ordering matches chronological
// ordering in SQLite (RFC3339Nano is variable-width and can misorder).
const tsFormat = "2006-01-02T15:04:05.000000000Z07:00"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps a SQLite database for cold storage of events and deployments.
// It maintains separate writer (single-conn) and reader (multi-conn) handles.
type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// Open creates a new Store backed by the SQLite database at path.
// Use ":memory:" for tests. Runs migrations automatically.
func Open(path string) (*Store, error) {
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

	s := &Store{writer: writer, reader: reader, path: path}

	if err := s.Migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("coldstore: migrate: %w", err)
	}

	slog.Info("coldstore opened", "path", path)
	return s, nil
}

// Migrate runs all embedded SQL migration files idempotently.
func (s *Store) Migrate() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	for _, entry := range entries {
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if _, err := s.writer.Exec(string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// Close closes both writer and reader database handles.
func (s *Store) Close() error {
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
