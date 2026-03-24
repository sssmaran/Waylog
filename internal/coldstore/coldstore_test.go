package coldstore

import (
	"testing"
)

func TestOpenClose(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := db.(*SQLiteStore)
	var journalMode string
	if err := raw.writer.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "memory" && journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal or memory", journalMode)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatal("second migrate failed:", err)
	}
}

func TestEventsTableExists(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := db.(*SQLiteStore)
	var count int
	err = raw.writer.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Fatal("events table not created:", err)
	}
}

func TestDeploymentsTableExists(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	raw := db.(*SQLiteStore)
	var count int
	err = raw.writer.QueryRow("SELECT COUNT(*) FROM deployments").Scan(&count)
	if err != nil {
		t.Fatal("deployments table not created:", err)
	}
}
