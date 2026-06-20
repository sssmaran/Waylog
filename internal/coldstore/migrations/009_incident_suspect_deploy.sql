-- Sticky suspect-deploy correlation: persisted so triage's Suspect Change does
-- not flicker as the capped evidence list churns across classifier ticks.
ALTER TABLE incidents ADD COLUMN suspect_deploy_id TEXT;
