# 05 – API

The API is described by [openapi.yaml](openapi.yaml) (OpenAPI 3.1). That file is the source of truth for paths, schemas and permissions. This file gives the rules that the schema cannot show. Feature rules: [F08](04-features/F08-config-as-code-api-cli-terraform.md).

## 1. Conventions

| Topic | Rule |
|---|---|
| Base path | `/api/v1`. UI, CLI and Terraform use the same paths. |
| Format | JSON, UTF-8, `snake_case` fields, RFC 3339 UTC times, ids are strings `<prefix>_<ULID>`. |
| Auth | `Authorization: Bearer pp_…` or the session cookie (with `X-CSRF-Token` on non-GET). See [10-security](10-security.md#1-authentication). |
| Permission | Each operation has `x-permission`. Values are the permissions in the [matrix](10-security.md#permission-matrix), plus `public` (no login) and `authenticated` (any logged-in user). |
| Errors | HTTP status plus body `{code, message, details, request_id}`. `code` is stable and snake_case. |
| Pagination | `limit` (1–200, default 50) and `cursor`. Responses: `{items, next_cursor}`. `next_cursor` is null at the end. Short lists (under 200 items by nature) return a plain array. |
| Idempotency | Header `Idempotency-Key` on operations that start work (`deploy`, `rollback`, `move`, `promote`, `config apply`, `backup run`, `restore`, `restore check`, template instantiate). Stored `idempotency_key_ttl`. |
| Async | Operations that start work return `202` and the object. Progress: poll `GET` or use SSE. |
| Streams | SSE (`text/event-stream`) with `sse_keepalive`. Events carry an `id` so a client can resume with `Last-Event-ID`. WebSocket only for exec. |
| Rate limit | `api_rate_limit`. Over limit: `429` and `Retry-After`. |
| Concurrency | Mutable resources return `ETag`. `PATCH` accepts `If-Match`. A mismatch gives `412`. |
| Dry run | `dry_run: true` in the body of deploy, promote, config plan returns the plan and changes nothing. |
| Deletes | Destructive deletes of projects, services, orgs need a `confirm` query value equal to the name or slug. |
| Secrets | No response ever has a secret value. Write fields are `writeOnly` in the schema. |

## 2. Standard error codes

`validation_failed` (422), `unauthenticated` (401), `forbidden` (403), `not_found` (404), `conflict` (409), `rate_limited` (429), `internal_error` (500). Specific 409/422 codes used in the spec (the list is closed; new codes are added here first):

`domain_in_use`, `name_conflict`, `managed_by_git`, `last_owner`, `plan_changed`, `already_live`, `release_image_pruned`, `secret_version_destroyed`, `deployment_not_cancellable`, `node_not_empty`, `missing_variables`, `unresolved_reference`, `reference_cycle`, `template_unsafe`, `release_not_promotable`, `domain_port_mismatch`, `idempotency_key_reuse`, `zero_downtime_not_available` (a warning, not an error), `missing_secret`.

## 3. Resource map

| Area | Paths (see openapi.yaml) |
|---|---|
| System, setup, auth | `/healthz`, `/version`, `/setup/first-user`, `/auth/*`, `/me` |
| Org, members, teams, tokens, SSO, audit | `/org`, `/members`, `/teams`, `/tokens`, `/sso-providers`, `/audit-events`, `/audit/verify` |
| Nodes | `/nodes`, `/nodes/{id}/…` |
| Hierarchy | `/projects`, `/projects/{id}/environments`, `/environments/{id}`, `/environments/{id}/services`, `/services/{id}` |
| Deploy | `/services/{id}/deploy`, `/rollback`, `/move`, `/deployments/{id}/…`, `/services/{id}/releases`, `/releases/{id}/…` |
| Promote and previews | `/releases/{id}/promote`, `/environments/{id}/promote`, `/projects/{id}/previews` |
| Config as code | `/projects/{id}/config`, `/config/plan`, `/config/apply` |
| Config data | `/projects/{id}/variables`, `/projects/{id}/secrets`, `/services/{id}/domains`, `/services/{id}/volumes` |
| Observability | `/services/{id}/logs`, `/logs/search`, `/services/{id}/metrics`, `/nodes/{id}/metrics`, `/events` |
| Templates, Git | `/templates`, `/git-connections`, `/webhooks/git/{id}` |
| Backups | `/backup-targets`, `/services/{id}/backup-policies`, `/backup-policies/{id}/…`, `/restore-checks` |
| Alerts | `/alert-channels`, `/alert-rules`, `/alert-instances` |

## 4. SSE event shapes

- Deployment stream (`/deployments/{id}/events`): `{type: state|health|log|canary_gate, state, data, time}`. Ends after a terminal state.
- Log stream: one `LogLine` per event.
- Platform stream (`/events`): `{topic, object_id, payload}`. Only objects the user may read are sent.

## 5. Webhooks in

- `POST /webhooks/git/{webhook_id}`: verifies the provider signature with the secret of the webhook. Provider is known from the webhook record. Handled events: push, pull request (opened, reopened, synchronize, closed). Replay protection: delivery id stored 24 h. Returns `202` after queueing. Unknown event types return `202` and are ignored.
- `POST /webhooks/deploy/{webhook_id}`: a CI deploy hook. Needs the header `X-Polipan-Token` that matches the secret. Triggers a deploy of the linked service.

## 6. Versioning and compatibility

- Within `/api/v1` only additive changes. CI runs an OpenAPI diff against the last release (see [13](13-testing-and-quality.md)).
- Clients ignore unknown fields. The server ignores unknown fields on write only when they are marked `x-ignored`; otherwise it returns 422.

## Requirements

- **REQ-API-100** (MUST) The server serves `openapi.yaml` at `/api/v1/openapi.yaml` and a docs page at `/api/docs`.
- **REQ-API-101** (MUST) The server refuses to start in CI tests if a registered route has no `x-permission` in the OpenAPI file, or if the OpenAPI file has an operation with no handler.
- **REQ-API-102** (MUST) `ETag` and `If-Match` are supported on service, environment, project, domain and alert-rule resources.
- **REQ-API-103** (MUST) SSE streams support `Last-Event-ID` resume for 5 minutes.
- **REQ-API-104** (MUST) The error code list in section 2 is complete for the spec. Handlers return only these codes.
- **REQ-API-105** (MUST) Responses never include secret values, password hashes, token hashes, or KEK/DEK material. A test serializes every response schema and checks for banned field names.
