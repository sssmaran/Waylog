-- 005_incident_evidence.sql: add v1.0 incident evidence snapshots
-- (PropagationSnapshot + BlastSnapshot). Both columns hold JSON text or NULL.
-- A NULL column round-trips to a nil *PropagationSnapshot / *BlastSnapshot in Go.

ALTER TABLE incidents ADD COLUMN propagation_json TEXT;
ALTER TABLE incidents ADD COLUMN blast_json       TEXT;
