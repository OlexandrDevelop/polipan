# 10 – Security

User view and acceptance criteria: [F07](04-features/F07-teams-rbac-audit-sso-secrets.md). Defaults: [glossary](01-glossary.md#defaults).

## 1. Authentication

| Method | Rules |
|---|---|
| Local user | Email + password. Hash is argon2id with the glossary parameters. Min length 12. Login is rate limited: 5 failed tries per 15 min per account and per IP, then a 15 min lock. |
| 2FA | TOTP (RFC 6238). 10 one-time recovery codes (stored hashed). An org can require 2FA for all local users. |
| Session | Random 256-bit value in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie `pp_session`. Only its hash is stored. Lifetime `session_ttl`. State-changing requests with a cookie MUST send header `X-CSRF-Token`. |
| API token | `pp_` + 40 random chars. Shown once. Only SHA-256 is stored. Scopes: `read`, `deploy`, `write`, `admin`. A token never has more rights than its user. Optional project limit and expiry. Sent as `Authorization: Bearer`. |
| SSO | OIDC (generic and Google) with authorization code + PKCE, and GitHub OAuth2. See section 2. |
| Agent | mTLS certificate (see [09](09-multi-server.md)). Not valid for the public API. |
| Webhooks in | HMAC signature check (GitHub `X-Hub-Signature-256`, GitLab secret token, Gitea HMAC). Reject on mismatch, 401. Replay: delivery ids stored 24 h. |

Token scopes map to permissions: `read` = all `*:read`, `logs:read`, `metrics:read`; `deploy` = read + `service:deploy`, `service:rollback`, `service:promote`, `service:restart`; `write` = deploy + every non-admin write permission; `admin` = everything the user has.

First start: the installer prints a one-time URL to create the first `owner` user. After that the endpoint is closed.

### Host-admin access (recovery and provisioning)

Commands of `polipan-server admin` run only on the control-plane host. They need shell access to the server config and the database; there is no API for them. They never read, show or change the owner's password. Each change writes an `audit_events` row with `actor_type = system` and `actor_id = "polipan-server admin"`.

| Command | Rules |
|---|---|
| `reset-password <email>` | Prints a reset link for an existing user. One use, valid 1 h. Only its hash is stored. Audit `admin.reset_password`. |
| `invite-user <email> [--role viewer\|developer\|admin]` | Prints an invite link for a new user. One use, valid 1 h (a web invite lives 7 days). Only its hash is stored. Default role `viewer`; the `owner` role is refused (an owner promotes in the web app). Refused when the email is already a member or when no owner exists yet (then `setup-url` is the way). The user sets an own password and 2FA. Audit `member.invite` with the expiry. |
| `create-token <email> (--project <id or slug> \| --all-projects) [--scope read\|deploy\|write] [--expires d] [--name n]` | Makes an API token of an existing, enabled user. Default scope `deploy`, default life 24 h, max 90 days; the `admin` scope is refused. A project limit or an explicit `--all-projects` is required. The value is printed once; only its SHA-256 is stored. The token never has more rights than the user (section 1). Audit `token.create` with scopes, project and expiry. |
| `revoke-token <token id>` | Revokes any token. Audit `token.revoke`. |
| `remove-user <email>` | Removes a member who is not an owner: sessions and tokens end at once, the user row is soft deleted. Audit `member.remove`. |

Printed links and tokens are secrets: the operator copies them from the terminal and does not keep them in shell logs or tickets.

## 2. SSO

- Providers per org (`sso_providers`): `oidc` (any issuer with discovery), `google` (OIDC preset), `github` (OAuth2, scopes `read:user user:email read:org`).
- Login: the user picks the provider. The server verifies `state`, `nonce` (OIDC) and PKCE. The identity is matched by `(provider, subject)`. If none, the user is matched by verified email to an existing user (only if the provider says `email_verified = true`), else created if the email domain is in `allowed_domains` (or the list is empty and "auto-create" is on). For Google the `hd` claim must also be in `allowed_domains` when the list is not empty. For Google and GitHub, "auto-create" with a `default_role` and an empty `allowed_domains` is refused when the provider is saved (422 `unsafe_jit`) and again at login, because anybody has such an account. A `default_role` is never `owner`.
- **Group to role mapping:** on every login, the groups from `groups_claim` (OIDC) or the GitHub org teams are compared to `sso_group_mappings`. The user's `memberships` and `project_roles` with `source = sso` are replaced to match. Manual roles are not touched. If a user matches several mappings, the highest role wins per scope. If none match, the user gets no access unless a default role is set on the provider: the login is refused with the message "no access" and no session is made (a user without a role cannot have a session, because the role is read from the membership). A new user is not created in this case. Only an owner can map a group to `owner`.
- `orgs.sso_required = true` blocks password login for everyone except `owner` users who have `allow_owner_password` on (default on, always needs 2FA). Every such login writes an audit event `auth.break_glass`.
- Removing a user from the IdP group removes access at the next login. Sessions of SSO users have the normal TTL. An admin can revoke sessions at once.

## 3. Roles and permissions

Roles are fixed: `owner`, `admin`, `developer`, `viewer` (org level). A project can override a user's or team's role for that project with `project_roles` (`admin`, `developer`, `viewer`).

**Effective role in a project:** the highest role among the user's project role, team project roles, and the org role limited as follows: org `owner` and `admin` always keep `admin` or `owner` everywhere (an override cannot lower them). An org `developer` or `viewer` can be raised or lowered by an override.

**Protected environments:** an environment with `protected = true` marks permissions with `P` in the matrix. For `P` permissions on a protected environment, the role must be `admin` or higher. `developer` is denied. A change of `protected` itself needs `admin`.

**Auto deploy** (Git push, PR events) is a trigger set up by someone with `service:update`. On a protected environment, only `admin` can enable it. When it fires, the actor is `system` with the id of the git trigger, and the audit event names the commit author.

### Permission matrix

`O` = owner, `A` = admin, `D` = developer, `V` = viewer. `Y` = allowed, `-` = denied, `P` = allowed for `D` only on non-protected environments (always allowed for `A`/`O`). The permission names are exactly the values of `x-permission` in [openapi.yaml](openapi.yaml).

| Permission | O | A | D | V | Notes |
|---|---|---|---|---|---|
| `org:read` | Y | Y | Y | Y | |
| `org:update` | Y | Y | - | - | name, settings, retention |
| `org:delete` | Y | - | - | - | |
| `member:read` | Y | Y | Y | Y | |
| `member:manage` | Y | Y | - | - | an admin cannot change an owner |
| `team:manage` | Y | Y | - | - | |
| `sso:manage` | Y | Y | - | - | |
| `token:own` | Y | Y | Y | Y | create and revoke own tokens |
| `token:manage` | Y | Y | - | - | revoke tokens of others |
| `audit:read` | Y | Y | - | - | |
| `audit:export` | Y | Y | - | - | |
| `node:read` | Y | Y | Y | Y | |
| `node:manage` | Y | Y | - | - | add, cordon, drain, delete |
| `project:read` | Y | Y | Y | Y | |
| `project:create` | Y | Y | Y | - | |
| `project:update` | Y | Y | Y | - | |
| `project:delete` | Y | Y | - | - | |
| `env:read` | Y | Y | Y | Y | |
| `env:create` | Y | Y | Y | - | |
| `env:update` | Y | Y | P | - | `protected` flag needs `A` |
| `env:delete` | Y | Y | P | - | |
| `service:read` | Y | Y | Y | Y | |
| `service:create` | Y | Y | P | - | |
| `service:update` | Y | Y | P | - | |
| `service:delete` | Y | Y | P | - | |
| `service:deploy` | Y | Y | P | - | |
| `service:rollback` | Y | Y | P | - | |
| `service:promote` | Y | Y | P | - | `P` is judged on the **target** environment |
| `service:restart` | Y | Y | Y | - | allowed on protected too |
| `service:move` | Y | Y | P | - | |
| `service:exec` | Y | Y | P | - | terminal in a container |
| `logs:read` | Y | Y | Y | Y | |
| `metrics:read` | Y | Y | Y | Y | |
| `variable:read` | Y | Y | Y | Y | plain variables only |
| `variable:write` | Y | Y | P | - | |
| `secret:list` | Y | Y | Y | - | names, versions, no values |
| `secret:write` | Y | Y | P | - | create, new version, delete |
| `secret:destroy` | Y | Y | - | - | destroy an old version for good |
| `domain:manage` | Y | Y | P | - | |
| `volume:manage` | Y | Y | P | - | |
| `backup:read` | Y | Y | Y | Y | |
| `backup:manage` | Y | Y | P | - | policies, targets |
| `backup:restore` | Y | Y | - | - | restore into a service |
| `alert:read` | Y | Y | Y | Y | |
| `alert:manage` | Y | Y | - | - | |
| `template:read` | Y | Y | Y | Y | |
| `template:manage` | Y | Y | Y | - | custom templates |
| `git:manage` | Y | Y | - | - | Git connections and webhooks |
| `preview:manage` | Y | Y | Y | - | settings, delete a preview |
| `config:apply` | Y | Y | P | - | `polipan apply`, judged per target environment |
| `deployment:read` | Y | Y | Y | Y | |
| `deployment:cancel` | Y | Y | P | - | |

The API server checks the permission of every endpoint before any work, using its `x-permission` value (REQ-RBAC-100 below). An endpoint without it is a build error (see [13](13-testing-and-quality.md#3-ci-gates-all-must-pass-to-merge)).

## 4. Secrets

- **Key hierarchy:** KEK (master key, 32 random bytes, in `/etc/polipan/master.key` mode 0400 or env `POLIPAN_MASTER_KEY`, base64) → one DEK per project (`data_keys.dek_enc`, AES-256-GCM wrapped) → secret versions (`secret_versions`, AES-256-GCM, fresh 96-bit nonce per version, AAD = `secret_id || version`).
- **KEK rotation:** `polipan admin rotate-kek` re-wraps all DEKs. It does not re-encrypt secret values. DEK rotation (`rotate-dek --project`) re-encrypts that project's values. Both are server CLI commands run on the control-plane host, are audited, and are tested.
- **Write-only:** the API never returns a secret value. `GET` returns name, scope, current version, flags, timestamps and the actor who set it.
- **Versions:** each write makes a new version. A Release stores the version numbers it used. The last `retain_releases` versions are kept. Older versions not used by a retained Release can be destroyed (`secret:destroy`).
- **Use is audited:** a Release that resolves secrets writes `secret.use` (secret ids, versions, release id). Writes, deletes and destroys write their own events. Diffs show `***` for values.
- **Injection:** secrets are injected as container environment variables (default) or as files on a tmpfs mount (`secret_files` in config). Build-time secrets are only used if the secret has `build_time = true` and are passed as BuildKit secret mounts, never as build args, so they do not land in image layers.
- **Preview isolation:** a preview environment gets only variables and secrets with `preview = true`. Others are not resolved, not sent to the node, and a missing required name makes the preview fail with a clear message.
- **Log masking:** build and runtime logs are scanned for the values of the secrets of that service. Matches are replaced with `***` before storage (best effort for lines under 64 KiB; it is a safety net, not a guarantee).
- **Backup of keys:** the control-plane DB backup contains encrypted secrets. The master key is NOT in that backup. The installer tells the user to store `master.key` safely. Losing it means losing all secrets.
- **Not in v1:** external secret managers (Vault, OpenBao, cloud KMS). Planned after 1.0 (see roadmap), behind a `SecretProvider` interface in `/server/internal/secrets`.

## 5. Audit log

- Every state-changing API call, every login (success and failure), token use for admin actions, every secret write, role change, SSO config change, node join, failover and system-made deploy writes one `audit_events` row.
- Row: actor (type, id), action, target, project, diff (before/after, secrets masked), ip, user agent, request id, time.
- **Hash chain:** `hash = SHA-256(prev_hash || canonical_json(row fields without hash))`. The first row's `prev_hash` is 32 zero bytes. Writes are serialized with an advisory lock per org.
- **Verify:** `GET /api/v1/audit/verify` and `polipan audit verify` walk the chain and report the first broken `seq`. A daily job verifies and raises alert `audit_chain_broken` (critical).
- **Export:** JSON lines through the API (`audit:export`) and optional streaming to a webhook channel (a SIEM can pick it up).
- **Retention:** `audit_retention`. Older rows are exported to a signed archive segment and then removed. The chain keeps a checkpoint row with the last hash of the archive.
- The DB role of the server cannot UPDATE or DELETE `audit_events` (except the archive job role).

## 6. Threat model (STRIDE)

Assets: secrets, source code and images, user data in volumes, control-plane DB, node root, availability of apps.

| # | Area | Threat (STRIDE) | Mitigation | REQ |
|---|---|---|---|---|
| 1 | Agent join | Spoofing: someone uses a leaked join token | Token single use, 1 h TTL, only hash stored, CA fingerprint pinned in the install command, node appears as `pending` and an admin sees it in the UI/audit | REQ-AGENT-002 |
| 2 | Agent mTLS | Spoofing, tampering: a stolen agent cert | Certs valid 30 days, revocation list checked at connect, node delete revokes. The agent cert works only on the gateway, not on the API | REQ-AGENT-001 |
| 3 | Agent compromise | Elevation: attacker with root on one node | Node cannot read other nodes' secrets (desired state holds only that node's workloads). Node cannot talk to the API. WireGuard allowed-IPs limit mesh reach. Known limit: root on a node sees secrets of its workloads | REQ-SEC-100 |
| 4 | Server → agent commands | Tampering: forged commands | Commands only come through the mTLS stream from the server. The agent accepts only its server's CA | REQ-AGENT-001 |
| 5 | Git webhooks | Spoofing, tampering: fake push or PR event | Signature check, secret per connection, replay store, event only acted on if repo matches a linked service | REQ-SEC-101 |
| 6 | Preview environments | Information disclosure: PR code reads prod secrets | Only `preview = true` values are injected. Fork PRs off by default. Previews run on nodes selected by label `preview` if set (optional) and never mount prod volumes | REQ-PREV-010 |
| 7 | Templates and compose input | Elevation: template asks for privileged mode or host mounts | Template and compose input pass a validator: no `privileged`, no `cap_add` (except an allow-list: `NET_BIND_SERVICE`), no host network or pid, no bind mounts outside `/var/lib/polipan/binds/<service>`, no Docker socket mount, no `devices`. Fails with a list of violations | REQ-TPL-005 |
| 8 | Builds | Elevation, DoS: build runs untrusted code | BuildKit runs rootless (or unprivileged with default seccomp and AppArmor) in its own container without the Docker socket, on its own Docker network. Build steps get public internet egress only. Build has CPU/RAM/time limits (`build_timeout`). Build-time secrets only via BuildKit secrets. Builds of preview PRs from forks are off. VictoriaMetrics, VictoriaLogs, registry and PostgreSQL are not reachable from the build network | REQ-SEC-102, REQ-SEC-108 |
| 9 | Web terminal / exec | Elevation | Needs `service:exec` (protected env: admin). Every session is audited with start and end. Idle timeout 15 min | REQ-SEC-103 |
| 10 | API tokens | Spoofing: token leak | Hash only, scopes, expiry, last-used time, revoke, prefix `pp_` so scanners (GitHub secret scanning) can find it | REQ-AUTH-004 |
| 11 | Sessions | Spoofing: CSRF, XSS | HttpOnly + Secure cookie, SameSite=Lax, CSRF header, strict CSP (no inline scripts), output escaping in the SPA | REQ-AUTH-005 |
| 12 | SSO | Spoofing: account takeover by unverified email | Match by email only if `email_verified`. PKCE, state, nonce. Allowed domains | REQ-SSO-003 |
| 13 | Secrets store | Information disclosure | Envelope encryption, write-only API, masked logs and audit diffs, KEK not in DB backup | REQ-SEC-001 |
| 14 | Audit log | Repudiation, tampering | Append-only role, hash chain, daily verify, export | REQ-AUDIT-003 |
| 15 | Registry | Tampering: image replaced | Release pins a digest. Pull is by digest. Registry accessible only over the mesh and with per-node credentials | REQ-DEPLOY-103 |
| 16 | Caddy admin API | Elevation | Unix socket, mode 0600, owned by the agent. Not exposed on a TCP port | REQ-CORE-008 |
| 17 | Docker socket | Elevation | Only the agent mounts it. No service may mount it (template validator) | REQ-TPL-005 |
| 18 | SSRF | Information disclosure: user-supplied URLs (health check host, webhook channel URL, Git URL, OIDC issuer) hit internal addresses | Health checks run only against the service's own container address. Outbound webhook and Git URLs go through a deny list for loopback, link-local (169.254.0.0/16), the mesh CIDR and the Docker networks, unless org setting `allow_private_targets` is on | REQ-SEC-104 |
| 19 | Backups | Information disclosure: backup target holds data | restic repositories are encrypted with a per-policy password stored as a secret. Target credentials are secrets | REQ-BKP-004 |
| 20 | Installer | Tampering: supply chain | Install script served over HTTPS, binaries signed (cosign keyless), checksums verified by the script, SBOM published | REQ-OSS-004 |
| 21 | Control-plane DoS | Denial of service | Rate limits (`api_rate_limit`), request size limits, login rate limits, SSE connection limit per user (10) | REQ-SEC-105 |
| 22 | Multi-tenant mix | Elevation: project A reads project B | Every query is scoped by org and project in the data layer. Per-project DEK. The agent firewall (nftables) lets a container reach only containers of its own environment, plus the ingress Caddy. Services in different environments cannot reach each other except through public domains | REQ-SEC-106 |
| 23 | Log injection | Tampering | Logs stored as structured fields. The UI escapes content | REQ-OBS-101 |
| 24 | Restore checks | Information disclosure: restore on a shared node | The temp container has no ports, no network except the mesh-less default bridge without egress, and is removed after the check | REQ-BKP-104 |

## Requirements

- **REQ-AUTH-100** (MUST) Password hashing, session cookie flags, CSRF, login rate limits and lockout follow section 1.
- **REQ-AUTH-101** (MUST) API tokens follow section 1: hashed, scoped, expiring, revocable, never more rights than the user.
- **REQ-AUTH-102** (MUST) Host-admin access follows section 1 "Host-admin access": invite links and tokens made on the host are shown once, stored hashed, short lived (1 h invite, token default 24 h and max 90 days), never give the `owner` role or the `admin` scope, never change the owner's password, and are audited as the system.
- **REQ-RBAC-100** (MUST) Every endpoint declares `x-permission`. The server checks it first. The matrix in section 3 is the only source of what each role may do.
- **REQ-RBAC-101** (MUST) The effective role rule and the protected environment rule of section 3 are enforced in one authorization function used by all handlers.
- **REQ-SEC-100** (MUST) A node receives only the workloads, ingress routes and secrets for its own placements.
- **REQ-SEC-101** (MUST) Inbound webhooks verify signatures and reject replays.
- **REQ-SEC-102** (MUST) Builds run isolated with limits, without the Docker socket. `buildkitd` runs rootless, or unprivileged with the default seccomp and AppArmor profiles.
- **REQ-SEC-108** (MUST) Build network isolation. `buildkitd` is on its own Docker network, not the control-plane compose network. Build steps get egress to the public internet only: nftables on the control-plane host drops traffic from the build network to loopback, the bridge gateway, the control-plane compose network, `mesh_cidr`, link-local and RFC 1918 ranges (unless the org setting `allow_private_targets` is on). The registry push is done by the server side, not by build steps. VictoriaMetrics, VictoriaLogs, the registry and PostgreSQL bind only to the internal control-plane network and require credentials where the product supports them (registry: htpasswd or token auth; VictoriaMetrics and VictoriaLogs: a reverse proxy with basic auth in front of the write and query ports).
- **REQ-SEC-103** (MUST) Exec sessions are permission checked, audited and time limited.
- **REQ-SEC-104** (MUST) Outbound requests to user-supplied URLs follow the SSRF deny list.
- **REQ-SEC-105** (MUST) Rate limits and size limits exist as in section 6, row 21.
- **REQ-SEC-106** (MUST) Data access is scoped by org and project, and the agent firewall isolates environments from each other.
- **REQ-SEC-107** (MUST) Secrets follow the rules of section 4 (hierarchy, write-only, versions, masking, preview isolation).
- **REQ-AUDIT-100** (MUST) Audit events follow section 5 including the hash chain and the daily verify job.
- **REQ-SSO-100** (MUST) SSO follows section 2.
