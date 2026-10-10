-- SPDX-License-Identifier: Apache-2.0
-- Baseline migration (M0): identity tables, projects and the job queue.
-- The DDL is copied from docs/spec/03-data-model.md (sections 3.1, 3.2 and 3.7).

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE orgs (
  id text PRIMARY KEY,                       -- org_
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  sso_required boolean NOT NULL DEFAULT false,
  settings jsonb NOT NULL DEFAULT '{}',      -- retention overrides, default domain suffix, preview wildcard domain
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id text PRIMARY KEY,                       -- usr_
  email citext NOT NULL UNIQUE,
  name text NOT NULL,
  password_hash text,                        -- argon2id, null for SSO-only users
  totp_secret_enc bytea,                     -- encrypted with the server KEK
  totp_enabled boolean NOT NULL DEFAULT false,
  recovery_codes_hash text[] NOT NULL DEFAULT '{}',
  disabled boolean NOT NULL DEFAULT false,
  last_login_at timestamptz,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE memberships (
  id text PRIMARY KEY,                       -- mem_
  org_id text NOT NULL REFERENCES orgs(id),
  user_id text NOT NULL REFERENCES users(id),
  role text NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
  source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','sso')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, user_id)
);

CREATE TABLE invitations (
  id text PRIMARY KEY,
  org_id text NOT NULL REFERENCES orgs(id),
  email citext NOT NULL,
  role text NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
  token_hash text NOT NULL UNIQUE,
  invited_by text REFERENCES users(id),
  expires_at timestamptz NOT NULL,           -- 7 days
  accepted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE password_resets (
  token_hash text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,           -- 1 hour
  used_at timestamptz
);

CREATE TABLE teams (
  id text PRIMARY KEY,                       -- team_
  org_id text NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE team_members (
  team_id text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id),
  PRIMARY KEY (team_id, user_id)
);

CREATE TABLE projects (
  id text PRIMARY KEY,                       -- prj_
  org_id text NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  slug text NOT NULL,
  settings jsonb NOT NULL DEFAULT '{}',      -- retain_releases, preview_* defaults, build_timeout
  config_source jsonb,                       -- {connection_id, repo, branch, path} when managed by git
  config_sync_state text NOT NULL DEFAULT 'in_sync' CHECK (config_sync_state IN ('in_sync','drifted','plan_pending','apply_failed')),
  config_synced_commit text,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug)
);

-- Project-level role override. Exactly one of user_id or team_id is set.
CREATE TABLE project_roles (
  id text PRIMARY KEY,
  project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id text REFERENCES users(id),
  team_id text REFERENCES teams(id),
  role text NOT NULL CHECK (role IN ('admin','developer','viewer')),
  CHECK ((user_id IS NULL) <> (team_id IS NULL))
);

CREATE TABLE sessions (
  id text PRIMARY KEY,                       -- hash of the cookie value
  user_id text NOT NULL REFERENCES users(id),
  csrf_token_hash text NOT NULL,
  ip inet, user_agent text,
  expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_tokens (
  id text PRIMARY KEY,                       -- tok_
  org_id text NOT NULL REFERENCES orgs(id),
  user_id text NOT NULL REFERENCES users(id),
  name text NOT NULL,
  token_hash text NOT NULL UNIQUE,           -- SHA-256 of the full token, token starts with pp_
  token_prefix text NOT NULL,                -- first 8 chars, for display
  scopes text[] NOT NULL,                    -- see 10-security, e.g. {'read','deploy','admin'}
  project_id text REFERENCES projects(id),   -- optional: limit token to one project
  expires_at timestamptz,
  last_used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sso_providers (
  id text PRIMARY KEY,                       -- sso_
  org_id text NOT NULL REFERENCES orgs(id),
  kind text NOT NULL CHECK (kind IN ('oidc','google','github')),
  name text NOT NULL,
  issuer_url text,                           -- oidc only
  client_id text NOT NULL,
  client_secret_enc bytea NOT NULL,          -- encrypted with the server KEK
  scopes text[] NOT NULL DEFAULT '{openid,email,profile}',
  groups_claim text,                         -- claim name that holds groups
  allowed_domains text[] NOT NULL DEFAULT '{}',
  default_role text CHECK (default_role IN ('owner','admin','developer','viewer')),  -- null = no access without a mapping
  auto_create_users boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE sso_group_mappings (
  id text PRIMARY KEY,
  sso_provider_id text NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
  group_value text NOT NULL,                 -- IdP group or GitHub "org/team"
  role text NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
  project_id text REFERENCES projects(id),   -- null = org-level role
  UNIQUE (sso_provider_id, group_value, project_id)
);

CREATE TABLE sso_identities (
  provider_id text NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
  subject text NOT NULL,                     -- stable id from the IdP
  user_id text NOT NULL REFERENCES users(id),
  PRIMARY KEY (provider_id, subject)
);

CREATE TABLE jobs (
  id bigserial PRIMARY KEY,
  kind text NOT NULL,
  payload jsonb NOT NULL,
  state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','failed','dead')),
  attempts int NOT NULL DEFAULT 0,
  max_attempts int NOT NULL DEFAULT 5,
  run_at timestamptz NOT NULL DEFAULT now(),
  locked_by text, locked_at timestamptz,
  unique_key text,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX jobs_unique_key ON jobs (unique_key) WHERE state IN ('queued','running');
CREATE INDEX jobs_ready ON jobs (run_at) WHERE state = 'queued';
