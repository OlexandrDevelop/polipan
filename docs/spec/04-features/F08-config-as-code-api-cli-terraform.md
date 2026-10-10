# F08 – Config as code, full API, CLI and Terraform provider — Gap 8

## Summary

Everything the UI can do is available through the API. The CLI and the Terraform provider use only that API. A project can be described in a `polipan.yaml` file in Git, with plan and apply like Terraform. Details: [05-api](../05-api.md), [06-cli](../06-cli.md), [07-config-as-code](../07-config-as-code.md), [openapi.yaml](../openapi.yaml).

## User stories

- **US-CAC-01** As a developer, I keep `polipan.yaml` in my repo and a push updates my services.
- **US-CAC-02** As a developer, I run `polipan plan` and see the changes before they apply.
- **US-API-01** As an integrator, I automate every action with an API token.
- **US-CLI-01** As a developer, I deploy, tail logs and roll back from my terminal and from CI.
- **US-TF-01** As a platform engineer, I manage nodes, projects, services, domains and secrets names with Terraform.

## Config as code

- **REQ-CAC-001** (MUST) File name `polipan.yaml` (also `.polipan.yaml`, `polipan.yml`). Schema: [polipan.schema.json](../schemas/polipan.schema.json), `version: 1`.
- **REQ-CAC-002** (MUST) The file describes one project: its environments (overrides), services, domains, volumes, variables (plain), secret **names** (never values), health checks, deploy strategy, resources, placement, failover, cron, previews, backups and alerts.
- **REQ-CAC-003** (MUST) Sync: the file is read from the project's tracked repo/branch/path (`projects.config_source`) on push (webhook) or by `polipan apply` from the CLI. Both run the same plan/apply code.
- **REQ-CAC-004** (MUST) **Plan before apply:** the server computes a diff (create, update, delete per resource and field). `polipan plan` and `POST /projects/{id}/config/plan` return it with no change. Apply needs the plan hash for safety.
- **REQ-CAC-005** (MUST) The file wins for the fields it sets. Those fields get `git_managed` and are read-only in the UI and return 409 `managed_by_git` from the API when changed by hand (unless the request has `override_git: true` with `admin`, which makes a drift record).
- **REQ-CAC-006** (MUST) Delete safety: a resource removed from the file is not deleted by default. It is shown as `unmanaged` in the plan. With `prune: true` in apply (and `admin`), it is deleted. Volumes and databases need an extra flag `prune_data: true`.
- **REQ-CAC-007** (MUST) Validation errors report file path, line and field. Unknown keys are errors.
- **REQ-CAC-008** (MUST) Applying a changed service config creates a Release and a Deployment (`trigger = config_apply`) through the normal engine. Changes that do not need a new Release (domains, replicas, alerts) apply directly.
- **REQ-CAC-009** (MUST) Export: `polipan export` / `GET /projects/{id}/config` creates a `polipan.yaml` from existing UI-made state (secrets as names only).
- **REQ-CAC-010** (SHOULD) Environment-aware: `environments.<name>` blocks override service fields per environment, and `${{env.NAME}}` style references are plain variable references (no code or templating language).
- **REQ-CAC-011** (MUST) Apply on a protected environment follows its permission rules (`config:apply`, `P`).

## API

- **REQ-API-001** (MUST) REST/JSON under `/api/v1`. [openapi.yaml](../openapi.yaml) (OpenAPI 3.1) is the source of truth. Server handlers, CLI client, Terraform provider and TypeScript client are generated from or checked against it.
- **REQ-API-002** (MUST) Every operation has `operationId`, `summary`, request and response schemas, error responses, and `x-permission`.
- **REQ-API-003** (MUST) Every action possible in the UI is possible in the API (parity test in [13](../13-testing-and-quality.md)).
- **REQ-API-004** (MUST) Errors use one object `{code, message, details, request_id}`. Lists use cursor pagination (`limit`, `cursor`, response `next_cursor`).
- **REQ-API-005** (MUST) `POST` requests that start work accept `Idempotency-Key`. The same key and body within `idempotency_key_ttl` returns the first response. A key with a different body is 422 `idempotency_key_reuse`.
- **REQ-API-006** (MUST) Live data uses SSE (`text/event-stream`): deployment events, logs, platform events. Terminal uses WebSocket.
- **REQ-API-007** (MUST) Long actions return `202` with the object (for example a `deployment`) to poll or stream. Rate limit `api_rate_limit`, with `429` and `Retry-After`.
- **REQ-API-008** (MUST) Breaking changes need `/api/v2`. Within v1 only additive changes are allowed. CI runs an OpenAPI diff check.
- **REQ-API-009** (SHOULD) Outbound webhooks for events (deploy finished, node down) through alert channels of kind `webhook`.

## CLI

- **REQ-CLI-001** (MUST) Binary `polipan` (Go, cobra). Config in `~/.config/polipan/config.yaml` (server URL, token). Env `POLIPAN_URL`, `POLIPAN_TOKEN` win.
- **REQ-CLI-002** (MUST) Commands cover all API areas ([06-cli](../06-cli.md)). Output formats: `--output table|json|yaml`. Exit codes: 0 ok, 1 error, 2 usage, 3 not found, 4 permission, 5 deployment failed (with `--wait`).
- **REQ-CLI-003** (MUST) `polipan deploy --wait` follows the deployment to a final state and returns the exit code of the result. Works in CI with a token.
- **REQ-CLI-004** (SHOULD) Shell completion (bash, zsh, fish) and `polipan login` with a browser device flow.

## Terraform provider

- **REQ-TF-001** (MUST) Provider `polipan/polipan` built with terraform-plugin-framework, using only the public API.
- **REQ-TF-002** (MUST) Resources: `polipan_node_token`, `polipan_project`, `polipan_environment`, `polipan_service`, `polipan_domain`, `polipan_volume`, `polipan_variable`, `polipan_secret`, `polipan_backup_target`, `polipan_backup_policy`, `polipan_alert_channel`, `polipan_alert_rule`, `polipan_team`, `polipan_member`, `polipan_sso_provider`, `polipan_api_token`. Data sources: `polipan_node`, `polipan_project`, `polipan_service`, `polipan_template`.
- **REQ-TF-003** (MUST) Secret values are `sensitive`, write-only to the API, and stored in the Terraform state only as given input (the provider cannot read them back; drift detection uses the secret version number).
- **REQ-TF-004** (MUST) Import is supported for all resources by id.
- **REQ-TF-005** (SHOULD) Documentation and examples are generated (`tfplugindocs`) and published to the Terraform Registry.

## States

Config sync state per project: `in_sync`, `drifted` (fields changed by `override_git`), `plan_pending`, `apply_failed`. Shown in the UI.

## Edge cases

- Two pushes in a row: the later commit's apply waits for the earlier, in order.
- Invalid file on push: no apply, an error is posted to the commit status (if the provider supports it) and shown in the UI. Existing services are untouched.
- File and UI both change the same field: the file wins (REQ-CAC-005).
- A secret named in the file does not exist: apply fails with `missing_secret` listing names (secrets are set out of band).
- Service renamed in the file: it is a delete plus create in the plan, flagged with a hint (`rename` is not automatic).
- The same file applied to two projects: refused. One file belongs to one project.

## Touch points

[05-api](../05-api.md), [06-cli](../06-cli.md), [07-config-as-code](../07-config-as-code.md).

## Acceptance criteria

- **AC-CAC-01** Given a valid `polipan.yaml`, When `polipan plan` runs, Then it prints creates and changes and changes nothing. (REQ-CAC-004)
- **AC-CAC-02** Given a plan, When `polipan apply` runs, Then the services exist and match the file. (REQ-CAC-003)
- **AC-CAC-03** Given a field set by the file, When a user edits it in the UI or API, Then 409 `managed_by_git`. (REQ-CAC-005)
- **AC-CAC-04** Given a service removed from the file, When applied without `prune`, Then it still exists and the plan marks it `unmanaged`. (REQ-CAC-006)
- **AC-CAC-05** Given an unknown key in the file, When validated, Then the error has line and field. (REQ-CAC-007)
- **AC-CAC-06** Given secrets in the UI, When `polipan export` runs, Then the file has only secret names. (REQ-CAC-009)
- **AC-API-01** Given the OpenAPI file, When linted, Then every operation has `x-permission` and an `operationId`. (REQ-API-002)
- **AC-API-02** Given a repeated POST with the same `Idempotency-Key`, When sent twice, Then one deployment exists. (REQ-API-005)
- **AC-API-03** Given each UI action in the parity list, When tested, Then an API call does the same. (REQ-API-003)
- **AC-CLI-01** Given a failing deployment, When `polipan deploy --wait` ends, Then exit code is 5. (REQ-CLI-003)
- **AC-CLI-02** Given `--output json`, When any list command runs, Then valid JSON is printed. (REQ-CLI-002)
- **AC-TF-01** Given a Terraform config with a project and a service, When `terraform apply` runs, Then they exist, and a second plan shows no change. (REQ-TF-001, REQ-TF-002)
- **AC-TF-02** Given a `polipan_secret`, When state is read, Then the value is marked sensitive and the API returns no value. (REQ-TF-003)

## Out of scope

Pulumi and CDK providers (can be generated from the API later). A Kubernetes operator.
