# F07 – Teams, roles, audit log, SSO and secrets — Gap 7

## Summary

Four parts: (7a) users, teams and roles; (7b) audit log; (7c) single sign-on; (7d) secrets management. Technical detail, the permission matrix and the threat model are in [10-security](../10-security.md). Tables: [03-data-model](../03-data-model.md).

## User stories

- **US-RBAC-01** As an owner, I invite people and give each one a role.
- **US-RBAC-02** As an admin, I make a team and give it a role on one project only.
- **US-RBAC-03** As an owner, I protect production so developers cannot change it.
- **US-AUDIT-01** As an admin, I see who changed what and when, and I can filter and export.
- **US-AUDIT-02** As a security lead, I can prove the log was not changed.
- **US-SSO-01** As an admin, I let people sign in with Google, GitHub or my OIDC provider.
- **US-SSO-02** As an admin, I map IdP groups to roles, and I can force SSO-only login.
- **US-SEC-01** As a developer, I store a secret and nobody can read it back, but my app gets it.
- **US-SEC-02** As an admin, I rotate a secret and roll back to the old version if needed.
- **US-AUTH-01** As a user, I sign in with a password and 2FA, and I create API tokens.

## 7a Teams, roles, authentication

- **REQ-AUTH-001** (MUST) Local login with email and password (argon2id). Rate limit and lockout as in [10](../10-security.md#1-authentication).
- **REQ-AUTH-002** (MUST) TOTP 2FA with recovery codes. An org setting can require it.
- **REQ-AUTH-003** (MUST) Sessions use an HttpOnly cookie with `session_ttl`. A user can list and revoke their sessions. An admin can revoke all sessions of a user.
- **REQ-AUTH-004** (MUST) API tokens: prefix `pp_`, shown once, hash stored, scopes (`read`, `deploy`, `write`, `admin`), optional project limit and expiry, last-used time, revoke. A token never exceeds its user's permissions.
- **REQ-AUTH-005** (MUST) Cookie flags, CSRF header, strict CSP as in [10](../10-security.md#1-authentication).
- **REQ-AUTH-006** (MUST) First start: a one-time setup URL creates the first `owner`. After that the setup endpoint returns 404.
- **REQ-AUTH-007** (MUST) Password reset: by email link if SMTP is configured, else by `polipan admin reset-password <email>` on the control-plane host.
- **REQ-RBAC-001** (MUST) Roles are fixed: `owner`, `admin`, `developer`, `viewer`, at org level. No custom roles in v1.
- **REQ-RBAC-002** (MUST) The permission matrix in [10](../10-security.md#permission-matrix) is the single rule set. Every API endpoint declares one `x-permission`.
- **REQ-RBAC-003** (MUST) Project role overrides for users and teams, with the effective-role rule of [10](../10-security.md#3-roles-and-permissions).
- **REQ-RBAC-004** (MUST) Protected environments: `protected` is on by default for `production`. On protected environments the permissions marked `P` need `admin`+. Changing `protected` needs `admin`.
- **REQ-RBAC-005** (MUST) Teams are groups of users in an org. A team can have a role on a project.
- **REQ-RBAC-006** (MUST) Invite by email (link valid 7 days) or create a user directly (admin sets a temporary password, forced change on first login). Remove user: sessions and tokens are revoked at once.
- **REQ-RBAC-007** (MUST) An org always keeps at least one `owner`. The last owner cannot be removed, demoted or disabled (409 `last_owner`).
- **REQ-RBAC-008** (MUST) An `admin` cannot change an `owner`'s role or remove an owner.

## 7b Audit log

- **REQ-AUDIT-001** (MUST) These actions write an audit event: all state-changing API calls, logins (ok and failed), logout, token create/revoke/use of admin scope, role and membership changes, SSO config, secret write/delete/destroy/use, node join/delete, deploy, rollback, promote, move, failover, preview create/delete, backup restore, config apply, exec session start/end, and audit export.
- **REQ-AUDIT-002** (MUST) Each event has actor (type and id), action, target (type and id), project, diff (before/after, secrets masked), ip, user agent, request id, time.
- **REQ-AUDIT-003** (MUST) The log is append-only and hash-chained (SHA-256). `GET /audit/verify` returns the first broken sequence or `ok`. A daily job verifies and alerts.
- **REQ-AUDIT-004** (MUST) Search and filter by actor, action, target, project, time range. Cursor pagination.
- **REQ-AUDIT-005** (MUST) Export as JSON lines and optional streaming to a webhook channel.
- **REQ-AUDIT-006** (MUST) Retention `audit_retention` with signed archive segments ([10](../10-security.md#5-audit-log)).
- **REQ-AUDIT-007** (MUST) Actions done by Polipan itself (auto-deploy, failover, preview cleanup) have `actor_type = system` and name the trigger (commit author, node id).

## 7c SSO

- **REQ-SSO-001** (MUST) Providers per org: generic OIDC (discovery URL), Google (OIDC preset), GitHub (OAuth2). Setup and "test login" in the UI.
- **REQ-SSO-002** (MUST) Group-to-role mapping (`sso_group_mappings`) for org-level and project-level roles, applied on every login. Mapped roles have `source = sso` and are replaced on each login. Manual roles stay.
- **REQ-SSO-003** (MUST) Accounts match by `(provider, subject)`. Linking by email only when `email_verified` is true. Allowed email domains per provider.
- **REQ-SSO-004** (MUST) `sso_required` blocks password login except owners with `allow_owner_password` (needs 2FA, audited as `auth.break_glass`).
- **REQ-SSO-005** (SHOULD) Just-in-time user creation on first SSO login, with the default role set on the provider (or no access).
- **REQ-SSO-006** (MUST) OIDC uses authorization code with PKCE, `state` and `nonce`. Client secrets are stored encrypted with the server KEK.

## 7d Secrets

- **REQ-SEC-001** (MUST) The built-in secrets store uses envelope encryption: KEK → one DEK per project → AES-256-GCM per secret version ([10](../10-security.md#4-secrets)).
- **REQ-SEC-002** (MUST) Secrets are write-only for every list and read call, the CLI and Terraform. The one exception is `revealSecret` (`secret:reveal`, owner and admin, browser session only, audited as `secret.reveal`, never cached). It is the "Show" button on the variables page.
- **REQ-SEC-003** (MUST) Every write creates a new version. Releases record the version numbers. A user can see versions (number, who, when) and set an earlier version as current (creates a new version with the old value, so history stays linear). `secret:destroy` removes the ciphertext of an old version.
- **REQ-SEC-004** (MUST) Use is audited (`secret.use` per Release, with ids and versions). Reads of metadata are not secret reads and are not audited per call.
- **REQ-SEC-005** (MUST) Injection as env vars (default) or as tmpfs files; build-time secrets only via BuildKit secret mounts.
- **REQ-SEC-006** (MUST) Preview environments get only `preview = true` values ([F03](F03-preview-environments.md)).
- **REQ-SEC-007** (MUST) Build and runtime logs and audit diffs mask secret values.
- **REQ-SEC-008** (MUST) KEK rotation (`polipan admin rotate-kek`) and DEK rotation (`rotate-dek`) exist, are audited, and keep services running (values are re-wrapped or re-encrypted without a redeploy).
- **REQ-SEC-009** (MUST) The master key file is created at install with mode 0400, never logged, and not part of any backup. The installer prints a clear warning about keeping a copy.
- **REQ-SEC-010** (SHOULD) A `SecretProvider` interface in the server allows later backends (Vault, OpenBao). Only the built-in provider ships in v1.

## States

- User: `invited → active → disabled`.
- Secret version: `current → previous → destroyed`.
- Audit chain: `ok | broken(seq)`.

## Edge cases

- A user is removed from an SSO group while logged in: access changes at the next login. An admin can force revoke.
- Two mappings give both `viewer` and `developer`: highest wins per scope.
- IdP down: password login for owners (break-glass) keeps working if allowed.
- A token of a removed user stops working at once.
- Secret rotation while a canary runs: the running Release keeps its versions. A new Release uses the new version.
- Audit write fails: the action fails (an action without an audit row is not allowed), except the audit failure for login attempts, which are logged to the system log and retried.
- Lost master key: secrets cannot be decrypted. The UI shows "secrets unreadable" and docs describe recovery (re-enter secrets).

## Touch points

API: `/auth/*`, `/me`, `/members`, `/teams`, `/tokens`, `/sso-providers`, `/audit-events`, `/audit/verify`, `/projects/{id}/secrets`. CLI: `polipan login`, `polipan token`, `polipan audit`, `polipan secret`. UI: Settings → Members, Teams, SSO, Audit, Tokens.

## Acceptance criteria

- **AC-RBAC-01** Given a viewer, When they call any write endpoint, Then 403. (REQ-RBAC-002)
- **AC-RBAC-02** Given a developer on a protected production environment, When they deploy, Then 403, and on staging Then 202. (REQ-RBAC-004)
- **AC-RBAC-03** Given a project role override of `viewer` for an org developer, When they edit that project, Then 403. (REQ-RBAC-003)
- **AC-RBAC-04** Given the last owner, When they try to leave, Then 409 `last_owner`. (REQ-RBAC-007)
- **AC-AUTH-01** Given 5 failed logins, When the 6th comes within 15 min, Then the account is locked and the response is 429. (REQ-AUTH-001)
- **AC-AUTH-02** Given an API token with scope `read`, When used to deploy, Then 403. (REQ-AUTH-004)
- **AC-AUTH-03** Given the setup was done, When the setup URL is called again, Then 404. (REQ-AUTH-006)
- **AC-AUDIT-01** Given a variable change, When done, Then an audit event exists with the actor and a diff where secrets show `***`. (REQ-AUDIT-001, REQ-AUDIT-002)
- **AC-AUDIT-02** Given a changed audit row in the database, When `verify` runs, Then it reports the first broken sequence. (REQ-AUDIT-003)
- **AC-AUDIT-03** Given an auto-deploy from a push, When recorded, Then `actor_type = system` and the commit author is in the event. (REQ-AUDIT-007)
- **AC-SSO-01** Given a Google OIDC provider, When a user logs in with an allowed domain, Then a user is created with the mapped role. (REQ-SSO-001, REQ-SSO-002)
- **AC-SSO-02** Given an IdP that returns `email_verified = false` for an email that matches a local user, When logging in, Then no link is made and login is refused. (REQ-SSO-003)
- **AC-SSO-03** Given `sso_required` and a non-owner, When they use a password, Then 403. Given an owner with `allow_owner_password`, Then login works after 2FA and `auth.break_glass` is audited. (REQ-SSO-004)
- **AC-SEC-01** Given a stored secret, When any API GET is called, Then no value is returned. (REQ-SEC-002)
- **AC-SEC-02** Given a secret with two versions, When a Release is created, Then it records the current version number and a rollback uses that number. (REQ-SEC-003)
- **AC-SEC-03** Given a secret value in a log line, When stored, Then it is `***`. (REQ-SEC-007)
- **AC-SEC-04** Given `rotate-kek`, When done, Then all secrets still decrypt and no service restarted. (REQ-SEC-008)
- **AC-SEC-06** Given a Dockerfile `RUN` step that tries to connect to PostgreSQL, VictoriaLogs, VictoriaMetrics, the registry and a `mesh_cidr` address, When it builds, Then every connection fails and a public internet request works. (REQ-SEC-102, REQ-SEC-108)
- **AC-SEC-05** Given a database dump of the control-plane DB without `master.key`, When inspected, Then no secret value can be derived. (REQ-SEC-001, REQ-SEC-009)

## Out of scope

SCIM provisioning, custom roles, required approvals (Q-05), external secret managers (after 1.0), hardware keys (WebAuthn, later).
