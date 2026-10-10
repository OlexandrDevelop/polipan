-- SPDX-License-Identifier: Apache-2.0
-- M6: config as code. One row per file that a push (or a user) asks to sync. The rows of a project
-- are processed in seq order, so two pushes in a row apply in order (F08 edge cases).
-- projects.config_source, config_sync_state and config_synced_commit are from the baseline.

CREATE TABLE config_syncs (
  seq bigserial PRIMARY KEY,
  id text NOT NULL UNIQUE,                   -- cfs_
  project_id text NOT NULL REFERENCES projects(id),
  trigger text NOT NULL CHECK (trigger IN ('push','apply')),
  commit_sha text,
  author text,
  content text NOT NULL,                     -- the polipan.yaml of this commit
  state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','applied','plan_pending','invalid','failed','superseded','approved')),
  plan_hash text,
  plan jsonb,                                -- the plan (for plan_pending: what waits for an admin)
  result jsonb,                              -- per resource applied, skipped or failed
  errors jsonb,                              -- validation errors with line, path and message
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX config_syncs_project ON config_syncs (project_id, seq DESC);
CREATE INDEX config_syncs_queue ON config_syncs (project_id, seq) WHERE state = 'queued';
