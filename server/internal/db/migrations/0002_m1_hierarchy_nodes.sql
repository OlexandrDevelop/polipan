-- SPDX-License-Identifier: Apache-2.0
-- M1 migration: hierarchy, nodes, releases, deployments, domains, events.
-- The DDL is copied from docs/spec/03-data-model.md (sections 3.2 to 3.5 and the events table).
-- server_ca is an M1 addition (the server CA, see 03-data-model section 3.4).

CREATE TABLE environments (
  id text PRIMARY KEY,                       -- env_
  project_id text NOT NULL REFERENCES projects(id),
  name text NOT NULL,
  slug text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('production','staging','preview','custom')),
  protected boolean NOT NULL DEFAULT false,  -- true by default when kind = 'production'
  settings jsonb NOT NULL DEFAULT '{}',      -- strategy defaults per env, auto_deploy, preview_state
  base_env_id text REFERENCES environments(id),  -- preview: env it was cloned from
  preview_repo text,                         -- git_ connection repo full name, preview only
  preview_pr_number int,                     -- preview only
  preview_last_push_at timestamptz,
  preview_expires_at timestamptz,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, slug),
  CHECK (kind <> 'preview' OR (preview_repo IS NOT NULL AND preview_pr_number IS NOT NULL))
);
CREATE UNIQUE INDEX environments_one_per_pr
  ON environments (project_id, preview_repo, preview_pr_number) WHERE kind = 'preview' AND deleted_at IS NULL;

CREATE TABLE template_instances (
  id text PRIMARY KEY,
  template_id text NOT NULL,                 -- built-in template slug or custom template id
  template_version text NOT NULL,
  environment_id text NOT NULL REFERENCES environments(id),
  inputs jsonb NOT NULL DEFAULT '{}',        -- secret inputs are stored as secret refs
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE services (
  id text PRIMARY KEY,                       -- svc_
  environment_id text NOT NULL REFERENCES environments(id),
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('app','database','worker','cron')),
  source_type text NOT NULL CHECK (source_type IN ('git','image')),
  config jsonb NOT NULL,                     -- the live config, same shape as a config_snapshot (see 07)
  git_managed_fields text[] NOT NULL DEFAULT '{}',  -- field paths set by polipan.yaml
  template_instance_id text REFERENCES template_instances(id),
  db_engine text CHECK (db_engine IN ('postgres','mysql','mariadb','mongo','redis')),
  live_release_id text,                      -- FK added after releases (deferred)
  desired_state text NOT NULL DEFAULT 'running' CHECK (desired_state IN ('running','stopped')),
  failover text NOT NULL DEFAULT 'none' CHECK (failover IN ('none','auto')),
  allow_data_loss_failover boolean NOT NULL DEFAULT false,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (environment_id, name)
);

CREATE TABLE templates (
  id text PRIMARY KEY,                       -- tem_ (custom) ; built-in use slug in code
  org_id text REFERENCES orgs(id),           -- null = built-in
  slug text NOT NULL,
  version text NOT NULL,
  manifest jsonb NOT NULL,                   -- see C03
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug, version)
);

CREATE TABLE nodes (
  id text PRIMARY KEY,                       -- nod_
  org_id text NOT NULL REFERENCES orgs(id),
  name text NOT NULL,
  state text NOT NULL CHECK (state IN ('pending','ready','suspect','down','draining','cordoned','maintenance')),
  labels jsonb NOT NULL DEFAULT '{}',        -- e.g. {"ingress":"true","region":"eu"}
  public_ip inet, mesh_ip inet, mesh_subnet cidr,
  wireguard_public_key text,
  os text, arch text, docker_version text, agent_version text,
  capacity jsonb,                            -- {cpu_millis, memory_bytes, disk_bytes}
  last_heartbeat_at timestamptz,
  cert_serial text, cert_expires_at timestamptz,
  desired_state_version bigint NOT NULL DEFAULT 0,
  acked_state_version bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE node_join_tokens (
  id text PRIMARY KEY,
  org_id text NOT NULL REFERENCES orgs(id),
  token_hash text NOT NULL UNIQUE,
  node_name text NOT NULL,
  labels jsonb NOT NULL DEFAULT '{}',
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_by text REFERENCES users(id)
);

CREATE TABLE revoked_certs (
  serial text PRIMARY KEY,
  node_id text NOT NULL REFERENCES nodes(id),
  revoked_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE releases (
  id text PRIMARY KEY,                       -- rel_
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  number int NOT NULL,                       -- 1,2,3... per service, shown as v12
  image_ref text NOT NULL,                   -- repo@sha256:digest, always a digest
  image_digest text NOT NULL,
  config_snapshot jsonb NOT NULL,            -- ports, resources, health, volumes, domains, strategy, env refs
  variable_snapshot jsonb NOT NULL,          -- plain variables resolved at release time
  secret_versions jsonb NOT NULL,            -- {"DB_PASSWORD": 3}  names -> version numbers, never values
  source jsonb NOT NULL,                     -- {type, commit_sha, branch, pr_number, promoted_from_release_id, rollback_of_release_id, actor_id}
  created_by text REFERENCES users(id),
  pruned_at timestamptz,                     -- image removed from registry
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (service_id, number)
);

CREATE TABLE release_annotations (
  release_id text PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
  note text NOT NULL,
  updated_by text REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE services ADD FOREIGN KEY (live_release_id) REFERENCES releases(id);

CREATE TABLE deployments (
  id text PRIMARY KEY,                       -- dep_
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  environment_id text NOT NULL REFERENCES environments(id),
  release_id text REFERENCES releases(id),   -- null while state = queued/building
  previous_release_id text REFERENCES releases(id),
  trigger text NOT NULL CHECK (trigger IN ('manual','git_push','pull_request','promotion','rollback','api','config_apply','failover','move','schedule')),
  strategy text NOT NULL CHECK (strategy IN ('recreate','blue_green','canary')),
  strategy_options jsonb NOT NULL DEFAULT '{}',   -- canary steps, gates, drain_seconds
  state text NOT NULL CHECK (state IN ('queued','building','image_ready','starting','health_checking','shifting','switching','draining','live','failed','rolled_back','cancelled','superseded')),
  canary_step int,                           -- current percent when state = shifting
  target_node_id text REFERENCES nodes(id),  -- for move / failover
  failure_reason text,                       -- machine code, see 08
  failure_detail text,
  actor_id text REFERENCES users(id),
  idempotency_key text,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX deployments_one_active ON deployments (service_id)
  WHERE state IN ('building','image_ready','starting','health_checking','shifting','switching','draining');
CREATE INDEX deployments_service_created ON deployments (service_id, created_at DESC);

CREATE TABLE deployment_steps (
  id bigserial PRIMARY KEY,
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  state text NOT NULL,
  message text,
  data jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deployment_logs (
  deployment_id text NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  seq bigint NOT NULL,
  stream text NOT NULL CHECK (stream IN ('build','deploy','pre_deploy')),
  line text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (deployment_id, seq)
);

CREATE TABLE placements (
  id text PRIMARY KEY,
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  node_id text NOT NULL REFERENCES nodes(id),
  release_id text NOT NULL REFERENCES releases(id),
  replica_index int NOT NULL,
  color text NOT NULL CHECK (color IN ('blue','green')),
  role text NOT NULL CHECK (role IN ('live','candidate','draining','stopped')),
  container_id text,
  weight int NOT NULL DEFAULT 100 CHECK (weight BETWEEN 0 AND 100),
  health text NOT NULL DEFAULT 'unknown' CHECK (health IN ('unknown','starting','healthy','unhealthy')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX placements_node ON placements (node_id);
CREATE INDEX placements_service ON placements (service_id);

CREATE TABLE domains (
  id text PRIMARY KEY,                       -- dom_
  service_id text NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  host text NOT NULL,                        -- lower case, may start with *. only for preview wildcard
  path_prefix text NOT NULL DEFAULT '/',
  container_port int NOT NULL,
  https boolean NOT NULL DEFAULT true,
  options jsonb NOT NULL DEFAULT '{}',       -- max_body_bytes, ip_allow, basic_auth_secret (name), headers, compression
  redirect_www text CHECK (redirect_www IN ('to_www','to_apex')),
  ingress_node_ids text[],                   -- null = all ready nodes with label ingress=true
  tls_status text NOT NULL DEFAULT 'pending' CHECK (tls_status IN ('pending','issued','failed')),
  tls_expires_at timestamptz,
  tls_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (host, path_prefix)
);

CREATE TABLE events (
  id bigserial PRIMARY KEY,
  org_id text NOT NULL,
  topic text NOT NULL,                       -- deployment, node, service, backup, alert
  object_id text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE server_ca (
  id int PRIMARY KEY CHECK (id = 1),         -- one row
  cert_pem text NOT NULL,
  key_enc bytea NOT NULL,                    -- private key, AES-256-GCM with the server KEK
  created_at timestamptz NOT NULL DEFAULT now()
);
