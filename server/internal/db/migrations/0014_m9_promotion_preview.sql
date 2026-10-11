-- SPDX-License-Identifier: Apache-2.0
-- M9 migration: ordered promotion batches (REQ-PROMO-008) and the preview state helpers.
-- A project-wide promotion makes one queued deployment per service. A deployment with
-- after_deployment_id waits until that deployment is live. If that one ends in another state,
-- the waiting deployment is cancelled with the reason batch_stopped. The results of the batch
-- are the deployments that share batch_id (F04 states: no separate table in v1).

ALTER TABLE deployments ADD COLUMN batch_id text;
ALTER TABLE deployments ADD COLUMN after_deployment_id text REFERENCES deployments(id) ON DELETE SET NULL;
CREATE INDEX deployments_batch ON deployments (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX deployments_after ON deployments (after_deployment_id) WHERE after_deployment_id IS NOT NULL;

-- The sweep of previews reads the closing and expiring ones.
CREATE INDEX environments_preview_sweep ON environments (project_id) WHERE kind = 'preview' AND deleted_at IS NULL;
