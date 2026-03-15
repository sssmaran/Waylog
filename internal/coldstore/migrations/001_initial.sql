-- 001_initial.sql: events + deployments tables for SQLite cold storage.

PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id        TEXT NOT NULL,
    span_id         TEXT,
    parent_span_id  TEXT,
    event_name      TEXT NOT NULL,
    service         TEXT NOT NULL,
    env             TEXT NOT NULL,
    version         TEXT,
    deployment_id   TEXT,
    user_id         TEXT NOT NULL DEFAULT 'anonymous',
    user_tier       TEXT,
    flow            TEXT,
    status_code     INTEGER NOT NULL,
    success         INTEGER NOT NULL,
    error_code      TEXT,
    error_message   TEXT,
    latency_ms      INTEGER NOT NULL,
    timestamp       TEXT NOT NULL,
    ingested_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_events_trace   ON events (trace_id);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_events_service ON events (service, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_events_error   ON events (error_code, timestamp DESC)
    WHERE error_code IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_user    ON events (user_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS deployments (
    id         TEXT PRIMARY KEY,
    service    TEXT NOT NULL,
    version    TEXT,
    env        TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    last_seen  TEXT NOT NULL,
    metadata   TEXT
);

CREATE INDEX IF NOT EXISTS idx_deployments_svc ON deployments (service, first_seen DESC);
