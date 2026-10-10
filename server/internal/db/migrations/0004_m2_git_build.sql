-- SPDX-License-Identifier: Apache-2.0
-- M2 migration: Git connections, webhooks and their replay store, registry pull credentials,
-- build records and build caches.
-- git_connections is copied from docs/spec/03-data-model.md (section 3.7).
-- Git secret values (tokens, app keys, webhook secrets, deploy keys) are org or service values that
-- must never be injected into a container, so they are not rows of "secrets" (project scoped and
-- injected). They live in git_secrets, AES-256-GCM with a key made from the KEK.
-- git_connections.secret_id and webhook_secret_id stay NULL; auth holds the git_secrets ids.

CREATE TABLE git_connections (
  id text PRIMARY KEY,                       -- git_
  org_id text NOT NULL REFERENCES orgs(id),
  provider text NOT NULL CHECK (provider IN ('github','gitlab','gitea','generic')),
  name text NOT NULL,
  base_url text,
  auth jsonb NOT NULL,                       -- app id, installation id (no secrets)
  secret_id text REFERENCES secrets(id),     -- private key, token
  webhook_secret_id text REFERENCES secrets(id)
);

-- Encrypted Git secret values. ciphertext = AES-256-GCM(key, value), AAD = id.
CREATE TABLE git_secrets (
  id text PRIMARY KEY,                       -- gsc_
  org_id text NOT NULL REFERENCES orgs(id),
  nonce bytea NOT NULL,
  ciphertext bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Webhooks of one service (repos without a connection, or a deploy key). The URL is
-- /webhooks/git/{id}. Connection webhooks use the connection id instead.
CREATE TABLE git_webhooks (
  id text PRIMARY KEY,                       -- whk_
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('git','deploy_hook')),
  provider text NOT NULL CHECK (provider IN ('github','gitlab','gitea','generic')),
  secret_ref text NOT NULL REFERENCES git_secrets(id),
  last_delivery_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (service_id, kind)
);

-- Replay store: a delivery id is accepted once per webhook within 24 h (REQ-SEC-101).
CREATE TABLE git_webhook_deliveries (
  webhook_id text NOT NULL,
  delivery_id text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (webhook_id, delivery_id)
);
CREATE INDEX git_webhook_deliveries_time ON git_webhook_deliveries (received_at);

-- Per-node pull credentials of the built-in registry (htpasswd lines). The password is
-- derived from the KEK and never stored.
CREATE TABLE registry_credentials (
  username text PRIMARY KEY,
  node_id text REFERENCES nodes(id) ON DELETE CASCADE,  -- null: the server push user
  password_hash text NOT NULL,               -- bcrypt
  created_at timestamptz NOT NULL DEFAULT now()
);

-- The build of one deployment of a git service: the request and the result.
CREATE TABLE deployment_builds (
  deployment_id text PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
  commit_sha text,                           -- requested commit; the built commit after the build
  branch text,
  no_cache boolean NOT NULL DEFAULT false,
  builder text,
  image_ref text,
  image_digest text,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);

-- Build cache per service (REQ-APP-013). cleared_at is set by a deploy with no_cache.
CREATE TABLE build_caches (
  service_id text PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
  cache_key text NOT NULL,
  last_build_at timestamptz,
  cleared_at timestamptz
);
