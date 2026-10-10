-- SPDX-License-Identifier: Apache-2.0
-- M2 migration: volumes, variables, data keys, secrets and secret versions.
-- The DDL is copied from docs/spec/03-data-model.md (section 3.5).

CREATE TABLE volumes (
  id text PRIMARY KEY,                       -- vol_
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('volume','bind','tmpfs')),
  mount_path text NOT NULL,
  node_id text REFERENCES nodes(id),         -- node where the data lives (null for tmpfs)
  size_limit_bytes bigint,
  access_mode text NOT NULL DEFAULT 'single_writer' CHECK (access_mode IN ('single_writer','shared')),
  UNIQUE (service_id, name)
);

CREATE TABLE variables (
  id text PRIMARY KEY,                       -- var_
  project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  environment_id text REFERENCES environments(id) ON DELETE CASCADE,  -- null = shared
  service_id text REFERENCES services(id) ON DELETE CASCADE,          -- null = all services in scope
  name text NOT NULL,
  value text NOT NULL,
  preview boolean NOT NULL DEFAULT false,    -- true = also injected into preview environments
  build_time boolean NOT NULL DEFAULT false,
  git_managed boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX variables_scope_unique
  ON variables (project_id, COALESCE(environment_id,''), COALESCE(service_id,''), name);

CREATE TABLE data_keys (
  project_id text PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  dek_enc bytea NOT NULL,                    -- DEK encrypted with the KEK
  kek_id text NOT NULL,                      -- which KEK version wrapped it
  created_at timestamptz NOT NULL DEFAULT now(),
  rotated_at timestamptz
);

CREATE TABLE secrets (
  id text PRIMARY KEY,                       -- sec_
  project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  environment_id text REFERENCES environments(id) ON DELETE CASCADE,
  service_id text REFERENCES services(id) ON DELETE CASCADE,
  name text NOT NULL,
  preview boolean NOT NULL DEFAULT false,
  build_time boolean NOT NULL DEFAULT false,
  current_version int NOT NULL DEFAULT 1,
  git_managed boolean NOT NULL DEFAULT false,
  deleted_at timestamptz
);
CREATE UNIQUE INDEX secrets_scope_unique
  ON secrets (project_id, COALESCE(environment_id,''), COALESCE(service_id,''), name) WHERE deleted_at IS NULL;

CREATE TABLE secret_versions (
  secret_id text NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
  version int NOT NULL,
  nonce bytea NOT NULL,
  ciphertext bytea NOT NULL,                 -- AES-256-GCM, AAD = secret id || version
  created_by text REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  destroyed_at timestamptz,
  PRIMARY KEY (secret_id, version)
);
