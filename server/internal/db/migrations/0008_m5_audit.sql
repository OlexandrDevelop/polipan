-- SPDX-License-Identifier: Apache-2.0
-- M5: the hash-chained audit log, its archive segments and the TOTP replay guard.
-- The DDL of audit_events is from docs/spec/03-data-model.md.

CREATE TABLE audit_events (
  seq bigserial PRIMARY KEY,
  id text NOT NULL UNIQUE,                   -- aud_
  org_id text NOT NULL,
  actor_type text NOT NULL CHECK (actor_type IN ('user','token','system','agent','sso')),
  actor_id text,
  action text NOT NULL,                      -- e.g. service.deploy, secret.write, role.change
  target_type text NOT NULL,
  target_id text,
  project_id text,
  diff jsonb,                                -- {before, after}, secret values masked
  ip inet, user_agent text, request_id text,
  prev_hash bytea NOT NULL,
  hash bytea NOT NULL,                       -- SHA-256(prev_hash || canonical_json(row without hash))
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_org_time ON audit_events (org_id, created_at DESC);
CREATE INDEX audit_target ON audit_events (target_type, target_id);
CREATE INDEX audit_org_seq ON audit_events (org_id, seq);

-- The log is append-only. Only a session that sets polipan.audit_archive = 'on' (the archive job)
-- may update or delete a row. Tests that break a row on purpose set it too.
CREATE FUNCTION audit_events_append_only() RETURNS trigger AS $$
BEGIN
  IF current_setting('polipan.audit_archive', true) IS DISTINCT FROM 'on' THEN
    RAISE EXCEPTION 'audit_events is append-only';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_events_no_change BEFORE UPDATE OR DELETE ON audit_events
  FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
  FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();

-- A signed archive segment: the rows of an org from first_seq to last_seq, as gzip JSON lines.
-- last_hash is the checkpoint: the first row that stays has it as prev_hash.
CREATE TABLE audit_archives (
  id text PRIMARY KEY,                       -- aar_
  org_id text NOT NULL,
  first_seq bigint NOT NULL,
  last_seq bigint NOT NULL,
  row_count integer NOT NULL,
  prev_hash bytea NOT NULL,                  -- prev_hash of the first archived row
  last_hash bytea NOT NULL,                  -- hash of the last archived row
  data bytea NOT NULL,
  signature bytea NOT NULL,                  -- HMAC-SHA256 of data with a key derived from the KEK
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_archives_org ON audit_archives (org_id, last_seq);

-- The last used TOTP time step of a user, so a code cannot be used twice.
ALTER TABLE users ADD COLUMN totp_last_step bigint NOT NULL DEFAULT 0;
