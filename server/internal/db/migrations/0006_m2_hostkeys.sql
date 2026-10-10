-- SPDX-License-Identifier: Apache-2.0
-- M2 fix: SSH host key of the git source of a service (trust on first use).
-- The key is public, so it is stored as plain text. If the host of the repo changes,
-- the row is replaced at the next good clone.

CREATE TABLE git_host_keys (
  service_id text PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
  host text NOT NULL,
  host_key text NOT NULL,                    -- "type base64", like a known_hosts line without the host
  created_at timestamptz NOT NULL DEFAULT now()
);
