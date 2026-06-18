-- 006_incident_alert_evidence.sql: persist incident-attached alert evidence.

ALTER TABLE incidents ADD COLUMN alert_json TEXT;
