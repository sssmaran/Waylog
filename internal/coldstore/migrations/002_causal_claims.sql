-- 002_causal_claims.sql: causal inference claims (shadow mode).

CREATE TABLE IF NOT EXISTS causal_claims (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    claim_type    TEXT NOT NULL,
    subject       TEXT NOT NULL,
    target        TEXT NOT NULL,
    service       TEXT NOT NULL,
    confidence    REAL NOT NULL,
    tier          TEXT NOT NULL,
    evidence      TEXT NOT NULL,
    window_start  TEXT NOT NULL,
    window_end    TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    superseded_at TEXT,
    shadow_mode   INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_claims_active ON causal_claims (claim_type, subject, service) WHERE superseded_at IS NULL;
