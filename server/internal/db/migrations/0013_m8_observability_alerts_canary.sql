-- SPDX-License-Identifier: Apache-2.0
-- M8 migration: alert channels, rules and instances, the batch cursors of the log ingest, and the canary state.
-- The alert tables are from docs/spec/03-data-model.md section 3.7, with the M8 columns added.

CREATE TABLE alert_channels (
  id text PRIMARY KEY,                       -- chn_
  org_id text NOT NULL REFERENCES orgs(id),
  kind text NOT NULL CHECK (kind IN ('telegram','slack','email','webhook')),
  name text NOT NULL,
  config jsonb NOT NULL DEFAULT '{}',        -- chat id, recipients, smtp host (no secrets)
  secret_id text REFERENCES secrets(id),     -- bot token, webhook url, smtp password, hmac secret
  enabled boolean NOT NULL DEFAULT true,
  state text NOT NULL DEFAULT 'ok' CHECK (state IN ('ok','failing')),
  last_error text,
  last_ok_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE alert_rules (
  id text PRIMARY KEY,                       -- alr_
  org_id text NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  type text NOT NULL,                        -- see F09 rule types
  scope jsonb NOT NULL DEFAULT '{}',         -- {project_id, environment_id, service_id, node_id}
  params jsonb NOT NULL DEFAULT '{}',        -- threshold, for_seconds
  severity text NOT NULL CHECK (severity IN ('info','warning','critical')),
  channel_ids text[] NOT NULL DEFAULT '{}',
  repeat_interval_s int NOT NULL DEFAULT 14400,
  muted_until timestamptz,
  mute_reason text,
  enabled boolean NOT NULL DEFAULT true,
  builtin boolean NOT NULL DEFAULT false,    -- a default rule made for the org
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);
CREATE INDEX alert_rules_org_type ON alert_rules (org_id, type);

CREATE TABLE alert_instances (
  id text PRIMARY KEY,                       -- ali_
  rule_id text NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  fingerprint text NOT NULL,                 -- rule + target, for dedup
  state text NOT NULL CHECK (state IN ('firing','resolved')),
  first_fired_at timestamptz NOT NULL,
  last_notified_at timestamptz,
  resolved_at timestamptz,
  details jsonb,
  notify_count int NOT NULL DEFAULT 0,       -- firing message and reminders that were sent
  resolved_notified boolean NOT NULL DEFAULT false,
  suppressed_by text,                        -- the node id of a firing node_down alert that hides this one
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_id, fingerprint)
);
CREATE INDEX alert_instances_state ON alert_instances (state, first_fired_at DESC);

-- The engine reads the alert events one by one. The id of the last one it handled is saved here, so a server
-- restart does not send a message again (REQ-OBS-104).
CREATE TABLE alert_cursor (
  name text PRIMARY KEY,
  event_id bigint NOT NULL
);
CREATE INDEX events_alert ON events (id) WHERE topic = 'alert';

-- The last log batch of each node, so a batch that the agent sends twice is written once (12-observability section 7).
CREATE TABLE telemetry_batches (
  node_id text PRIMARY KEY,
  epoch text NOT NULL,
  last_batch bigint NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Canary state of a deployment. canary_step (percent) exists already.
ALTER TABLE deployments ADD COLUMN canary_paused boolean NOT NULL DEFAULT false;
ALTER TABLE deployments ADD COLUMN canary_gate jsonb;
ALTER TABLE deployments ADD COLUMN canary_step_started_at timestamptz;
ALTER TABLE deployments ADD COLUMN canary_extended boolean NOT NULL DEFAULT false;
ALTER TABLE deployments ADD COLUMN canary_paused_at timestamptz;
-- The green containers get traffic now (state shifting). The ingress of the nodes reads it, so an abort can take them out first.
ALTER TABLE deployments ADD COLUMN canary_routing boolean NOT NULL DEFAULT false;
