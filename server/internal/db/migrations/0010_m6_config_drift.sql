-- SPDX-License-Identifier: Apache-2.0
-- M6: a hand change of a field that polipan.yaml sets, made with override_git by an admin.
-- The project is marked drifted. The next apply reverts the field and closes the record.

CREATE TABLE config_drift (
  id text PRIMARY KEY,                       -- cdr_
  project_id text NOT NULL REFERENCES projects(id),
  target_type text NOT NULL,                 -- service, variable, secret, domain, volume
  target_id text NOT NULL,
  field text NOT NULL,
  actor_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE INDEX config_drift_open ON config_drift (project_id) WHERE resolved_at IS NULL;
