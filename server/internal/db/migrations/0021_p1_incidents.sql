-- SPDX-License-Identifier: Apache-2.0
-- P1-5: alerts that fire close together on one node are one incident, with the probable root cause first
-- (12-observability section 5.1).

CREATE TABLE incidents (
  id text PRIMARY KEY,                       -- inc_
  org_id text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  node_id text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  state text NOT NULL CHECK (state IN ('open','resolved')),
  root_instance_id text,                     -- the alert instance that is the probable cause
  root_type text,                            -- the rule type of that alert
  root_cause text,                           -- one line text of that alert, for the lists
  opened_at timestamptz NOT NULL,
  last_alert_at timestamptz NOT NULL,        -- the newest alert that joined; the window counts from it
  resolved_at timestamptz
);
-- One open incident per node.
CREATE UNIQUE INDEX incidents_one_open ON incidents (org_id, node_id) WHERE state = 'open';
CREATE INDEX incidents_org_state ON incidents (org_id, state, opened_at DESC);

ALTER TABLE alert_instances ADD COLUMN incident_id text REFERENCES incidents(id) ON DELETE SET NULL;
-- The message of the root alert of the incident told about this one, so it sends no message of its own.
ALTER TABLE alert_instances ADD COLUMN absorbed boolean NOT NULL DEFAULT false;
CREATE INDEX alert_instances_incident ON alert_instances (incident_id) WHERE incident_id IS NOT NULL;
