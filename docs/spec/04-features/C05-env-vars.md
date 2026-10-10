# C05 – Environment variables and secrets (core)

## Summary

Users set plain variables and secrets for a project, an environment or one service. Scope rules decide the final value. The secrets store itself (crypto, audit) is in [F07](F07-teams-rbac-audit-sso-secrets.md) and [10-security](../10-security.md#4-secrets). This file is the user feature.

## User stories

- **US-ENV-01** As a developer, I set `LOG_LEVEL` for all environments of a project and override it in production.
- **US-ENV-02** As a developer, I add a secret `STRIPE_KEY` and it never appears again in the UI or API.
- **US-ENV-03** As a developer, I import a `.env` file in bulk.
- **US-ENV-04** As a developer, I mark a value as build-time or as available in previews.
- **US-ENV-05** As a developer, I see which services use a variable and which Release has which value.

## Rules

- **REQ-ENV-001** (MUST) Scopes: `project` (shared), `environment`, `service`. Resolution order, last wins: project → environment → service ([03-data-model](../03-data-model.md#4-variable-and-secret-resolution)).
- **REQ-ENV-002** (MUST) Variable names match `[A-Za-z_][A-Za-z0-9_]*`, max 128 chars. Values max 64 KiB. A name is either a variable or a secret in one scope, never both (409 `name_conflict`).
- **REQ-ENV-003** (MUST) A variable or secret change does not restart the service by itself. The UI shows "pending changes" and the next Deployment (or "Redeploy with new config") creates a Release with the new values. A redeploy without a rebuild reuses the image digest.
- **REQ-ENV-004** (MUST) Each variable and secret has flags `build_time` (default false) and `preview` (default false).
- **REQ-ENV-005** (MUST) References: `${{VAR_NAME}}` inside a value is replaced by the resolved value of that name at Release time. `${{db.<name>.<FIELD>}}` references a database ([C02](C02-databases.md)). Cycles are rejected at save (422 `reference_cycle`).
- **REQ-ENV-006** (MUST) Bulk import accepts `.env` syntax and a JSON object. Items can be marked secret in the import UI. The API: `POST /projects/{id}/variables:bulk`.
- **REQ-ENV-007** (MUST) Secret values are write-only everywhere (API, UI, CLI, Terraform state). The Terraform provider marks them `sensitive` and stores a hash only.
- **REQ-ENV-008** (SHOULD) A "where used" view lists services and Releases that resolved a name and which version.
- **REQ-ENV-009** (MUST) Variables set by `polipan.yaml` have `git_managed = true` and are read-only in the UI ([F08](F08-config-as-code-api-cli-terraform.md)). Secrets are never in the file, only their names.
- **REQ-ENV-010** (MUST) Reserved names `POLIPAN_*` are set by Polipan (`POLIPAN_SERVICE`, `POLIPAN_ENV`, `POLIPAN_RELEASE`, `POLIPAN_COMMIT`, `POLIPAN_NODE`) and cannot be overwritten.

## Edge cases

- Secret deleted while a retained Release uses it: allowed, the version stays for rollback until destroyed (rollback of that Release then fails with `secret_version_destroyed` only if destroyed).
- Missing referenced variable: Release creation fails with 422 `unresolved_reference` and lists the names.
- Very long values or binary data: use secret files (`secret_files`, base64 input) instead of env.
- Preview environments only see `preview = true` values ([F03](F03-preview-environments.md)).

## Touch points

API: `/projects/{id}/variables`, `/projects/{id}/secrets`, bulk. CLI: `polipan var set|ls|import`, `polipan secret set|ls`. Config: `services.<name>.env`, `services.<name>.secrets` ([07](../07-config-as-code.md)).

## Acceptance criteria

- **AC-ENV-01** Given project var `A=1` and environment var `A=2`, When a Release is created in that environment, Then the snapshot has `A=2`. (REQ-ENV-001)
- **AC-ENV-02** Given a secret exists, When any API GET is called, Then the response has no value field. (REQ-ENV-007)
- **AC-ENV-03** Given a changed variable, When no deploy is done, Then running containers are unchanged and the UI shows pending changes. (REQ-ENV-003)
- **AC-ENV-04** Given a variable `A=${{B}}` and `B=${{A}}`, When saved, Then 422 `reference_cycle`. (REQ-ENV-005)
- **AC-ENV-05** Given a variable and a secret with the same name in one scope, When created, Then 409 `name_conflict`. (REQ-ENV-002)

## Out of scope

External secret managers (after 1.0). Per-user personal variables.
