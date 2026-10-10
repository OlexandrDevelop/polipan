-- SPDX-License-Identifier: Apache-2.0
-- M4 migration: backups with restore checks, restore jobs, and the database backup suggestion.
-- The DDL of the backup tables is copied from docs/spec/03-data-model.md (section 3.6).

ALTER TABLE services ADD COLUMN backup_skipped boolean NOT NULL DEFAULT false;
ALTER TABLE services ADD COLUMN db_version text;

CREATE TABLE backup_targets (
  id text PRIMARY KEY,                       -- bkp_
  org_id text NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('s3','b2','sftp','local','azure','gcs')),
  config jsonb NOT NULL,                     -- bucket, endpoint, path (no credentials)
  credentials_secret_id text NOT NULL REFERENCES secrets(id),
  last_test_at timestamptz, last_test_ok boolean,
  UNIQUE (org_id, name)
);

CREATE TABLE backup_policies (
  id text PRIMARY KEY,                       -- pol_
  service_id text REFERENCES services(id) ON DELETE CASCADE,  -- null = control-plane DB policy
  volume_id text REFERENCES volumes(id),
  schedule text NOT NULL,                    -- cron expression, UTC
  target_ids text[] NOT NULL,                -- one or more backup_targets
  retention jsonb NOT NULL,                  -- {keep_daily, keep_weekly, keep_monthly}
  repo_password_secret_id text NOT NULL REFERENCES secrets(id),
  restore_check_schedule text NOT NULL,
  restore_check_enabled boolean NOT NULL DEFAULT true,
  restore_check_mode text NOT NULL DEFAULT 'full' CHECK (restore_check_mode IN ('full','sample')),
  hooks jsonb NOT NULL DEFAULT '{}',         -- {pre: [...], post: [...]}
  enabled boolean NOT NULL DEFAULT true,
  org_id text NOT NULL REFERENCES orgs(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX backup_policies_control_plane ON backup_policies (org_id) WHERE service_id IS NULL;

CREATE TABLE backup_runs (
  id text PRIMARY KEY,                       -- bkr_
  policy_id text NOT NULL REFERENCES backup_policies(id) ON DELETE CASCADE,
  state text NOT NULL CHECK (state IN ('queued','running','succeeded','failed')),
  snapshots jsonb,                           -- [{target_id, restic_snapshot_id, size_bytes}]
  error text,
  started_at timestamptz, finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  trigger text NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule','manual','safety')),
  manifest jsonb,                            -- table count, row estimates, file count, size (REQ-BKP-102)
  node_id text
);

CREATE TABLE restore_checks (
  id text PRIMARY KEY,                       -- rck_
  policy_id text NOT NULL REFERENCES backup_policies(id) ON DELETE CASCADE,
  backup_run_id text NOT NULL REFERENCES backup_runs(id),
  target_id text NOT NULL REFERENCES backup_targets(id),
  node_id text REFERENCES nodes(id),
  state text NOT NULL CHECK (state IN ('queued','running','passed','failed')),
  checks jsonb,                              -- [{name, ok, detail}]
  duration_ms int,
  started_at timestamptz, finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  trigger text NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule','manual','first_backup'))
);

-- A restore of a snapshot into the service or into a new service (REQ-BKP-007).
CREATE TABLE backup_restores (
  id text PRIMARY KEY,                       -- brs_
  run_id text NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
  target_id text NOT NULL REFERENCES backup_targets(id),
  mode text NOT NULL CHECK (mode IN ('in_place','new_service')),
  service_id text REFERENCES services(id) ON DELETE SET NULL,      -- the restored service (the new one for new_service)
  state text NOT NULL CHECK (state IN ('queued','running','succeeded','failed')),
  error text,
  created_by text REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
