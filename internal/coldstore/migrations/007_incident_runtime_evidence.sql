-- 007_incident_runtime_evidence.sql: persist incident-attached runtime evidence
-- (infra: k8s OOMKill/crashloop; app: panics/unhandled rejections).

ALTER TABLE incidents ADD COLUMN runtime_json TEXT;
