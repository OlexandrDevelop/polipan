-- SPDX-License-Identifier: Apache-2.0
-- M3 migration: deploy engine. Warnings of a plan, health detail, placement ties to a deployment,
-- and the idempotency keys of 03-data-model section 3.8.

ALTER TABLE deployments ADD COLUMN warnings jsonb NOT NULL DEFAULT '[]';

-- The deployment that made the placement, and the time after which a stopped placement of a failed
-- deployment is removed (failed_green_keep). A kept old release has no expiry.
ALTER TABLE placements ADD COLUMN deployment_id text REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE placements ADD COLUMN expires_at timestamptz;
-- Last health result: consecutive_passes, consecutive_fails, last_error, last_status, last_body, checked_at.
ALTER TABLE placements ADD COLUMN health_info jsonb;

CREATE TABLE idempotency_keys (
  key text NOT NULL,
  actor_id text NOT NULL,
  request_hash bytea NOT NULL,
  response_status int NOT NULL,
  response_body jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (actor_id, key)
);

CREATE INDEX deployments_queued ON deployments (service_id, created_at) WHERE state = 'queued';
CREATE INDEX events_deployment ON events (object_id, id) WHERE topic = 'deployment';
