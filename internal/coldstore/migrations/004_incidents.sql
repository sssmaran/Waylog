-- 004_incidents.sql: v2.1 incident engine persistence.

CREATE TABLE IF NOT EXISTS incidents (
    incident_id              TEXT PRIMARY KEY,
    env                      TEXT NOT NULL,
    service                  TEXT NOT NULL,
    error_service            TEXT NOT NULL,
    error_step               TEXT NOT NULL,
    error_code               TEXT NOT NULL,
    status                   TEXT NOT NULL,
    cause                    TEXT NOT NULL,
    confidence               TEXT NOT NULL,
    severity                 INTEGER NOT NULL,
    started_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    last_seen_at             TEXT NOT NULL,
    recovering_at            TEXT,
    resolved_at              TEXT,
    affected_requests        INTEGER NOT NULL,
    affected_users           INTEGER,
    affected_services        INTEGER NOT NULL,
    top_services             TEXT,
    sample_traces            TEXT,
    evidence                 TEXT,
    next_checks              TEXT,
    instrumentation_warnings TEXT,
    lift                     REAL NOT NULL DEFAULT 0,
    baseline_count           INTEGER NOT NULL DEFAULT 0,
    current_count            INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_incidents_status_started ON incidents (status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_incidents_family_started ON incidents (env, service, error_step, error_code, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_incidents_resolved_at ON incidents (resolved_at);
