-- 003_signals.sql: production-context signal storage.

CREATE TABLE IF NOT EXISTS signals (
    signal_id   TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    source      TEXT NOT NULL,
    service     TEXT NOT NULL,
    env         TEXT NOT NULL,
    severity    TEXT NOT NULL,
    reason      TEXT NOT NULL,
    message     TEXT,
    resource    TEXT,
    metadata    TEXT,
    extra       TEXT,
    timestamp   TEXT NOT NULL,
    received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_signals_service_env_type_ts ON signals (service, env, type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_signals_ts ON signals (timestamp);
