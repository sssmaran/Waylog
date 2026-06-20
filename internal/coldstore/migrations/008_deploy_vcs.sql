-- Deploy provenance: commit/PR pushed by CI at deploy time. Optional, opaque,
-- vendor-neutral (pr_url is a full URL; works for GitHub/GitLab/Bitbucket).
ALTER TABLE deployments ADD COLUMN commit_sha    TEXT;
ALTER TABLE deployments ADD COLUMN pr_url        TEXT;
ALTER TABLE deployments ADD COLUMN commit_author TEXT;
