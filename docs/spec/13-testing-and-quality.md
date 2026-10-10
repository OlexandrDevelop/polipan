# 13 – Testing and Quality

Build agents write tests together with code. A requirement is done only when its acceptance criteria have a passing automated test. Mapping from requirements to milestones and tests: [traceability](traceability.md).

## 1. Test ids

- Every acceptance criterion in a feature file (`AC-<AREA>-NN`) is a test scenario. The test name MUST contain the AC id (for example a Go test named `TestAC_DEPLOY_01_BlueGreenNoFailedRequests`, or a Playwright title `AC-DEPLOY-01 ...`). Then `grep` finds the test for each AC.
- Cross-feature end-to-end scenarios have ids `T-E2E-NN` and are listed in section 4.
- Unit tests that cover one REQ name it in a comment or test name (`REQ-DEPLOY-102`).

## 2. Test levels

| Level | Scope | Tools | Runs |
|---|---|---|---|
| Unit | Pure logic: scheduler, state machines, plan/diff, config merge, permission function, secret crypto, hash chain | Go `testing`, table tests, fuzz for parsers (config, compose, webhooks) | Every PR |
| Component | One module with real PostgreSQL (testcontainers), fake agent, fake Git provider | Go `testing` | Every PR |
| API contract | Handlers against `openapi.yaml`: every operation reachable, request/response match schema, `x-permission` enforced with all four roles | Go + OpenAPI validator | Every PR |
| Agent integration | Real Docker and Caddy on a CI VM, agent reconcile, health checks, `POST /load` switch, WireGuard mesh with 2–3 nodes | Go + Docker-in-VM (or Lima/Vagrant) | Every PR (smoke), nightly (full) |
| E2E | Whole system on 3 VMs (control plane + 2 nodes) driven through the API and the UI | Go test harness, Playwright for UI | Nightly and before release |
| Chaos | Kill agent, kill server, drop network, fill disk, kill node while deploying | Scripted fault injection in the E2E harness | Nightly |
| Security | Secret leakage scan of every API response/log/audit row, permission matrix fuzz, dependency and license scan, container image scan | Custom Go tests, `govulncheck`, Trivy, license checker | Every PR (scan), nightly (full) |
| Performance | 200 services, 20 nodes, 50 deploys/hour on a lab setup; log search 1 GB in 3 s | k6 and custom load tool | Before release |
| Upgrade | Install 1.x, create data, upgrade to next version, check data and running services | E2E harness | Before release |

## 3. CI gates (all MUST pass to merge)

1. `go vet`, `staticcheck`, `golangci-lint`; `eslint` and `tsc --noEmit` for the web app. Formatting (`gofmt`, `prettier`) checked.
2. Unit, component and API contract tests. Coverage of `server/internal/deploy`, `scheduler`, `rbac`, `secrets`, `audit`, `config` packages ≥ 85 % lines. Others ≥ 70 %.
3. OpenAPI lint (`redocly lint` or equivalent) and the **diff check**: no breaking change inside `/api/v1`.
4. `x-permission` check: every operation has it, and its value is in the matrix of [10-security](10-security.md#permission-matrix) or is `public`/`authenticated`.
5. Schema checks: `polipan.schema.json` and `template.schema.json` are valid JSON Schema 2020-12. The examples in [07](07-config-as-code.md#8-examples) validate. A fixture with an unknown key fails.
6. Protocol check: `agent.proto` compiles. The generated code is up to date (CI regenerates and fails on a diff). `buf breaking` against the last release.
7. Migration check: migrations apply on an empty DB and on the previous release's DB. The schema equals the DDL baseline of [03-data-model](03-data-model.md) plus later migrations.
8. License check ([REQ-OSS-002](04-features/F11-open-source.md#rules)) and SPDX header check.
9. Secret leak test: a test creates secrets, deploys, and then scans all API responses, logs, audit rows and events for the secret values. It MUST find none.
10. Spec traceability: every `REQ-*` in the spec appears in [traceability](traceability.md), and every `AC-*` has a test whose name contains its id (the check runs once code exists; for the spec itself see [README](README.md#consistency-checks)).
11. Container image build for server, agent. Smoke: start, `/healthz` ok.

## 4. Cross-feature E2E scenarios

| Id | Scenario | Covers (ACs) |
|---|---|---|
| T-E2E-01 | Fresh install on 1 VM. Setup owner. Add an image service with a domain and HTTPS (staging ACME CA). | AC-APP-01, AC-DOM-01, AC-AUTH-03 |
| T-E2E-02 | Git app deploys on push (fake Git server), 3 releases, rollback to v2 with fast rollback under 10 s. | AC-APP-03, AC-REL-01, AC-REL-02, AC-REL-04 |
| T-E2E-03 | Zero downtime: 20 req/s load while blue-green deploying 5 times in a row. No failed request. Same with a failing health check (blue keeps serving). | AC-DEPLOY-01, AC-DEPLOY-02 |
| T-E2E-04 | Canary: bad version returns 5xx, gate trips at step 1, traffic returns to blue. Good version goes 10→25→50→100. | AC-DEPLOY-03 |
| T-E2E-05 | Staging to production promote with different domains and secrets. Same digest, no build, production secrets untouched. | AC-PROMO-01, AC-PROMO-02, AC-PROMO-05 |
| T-E2E-06 | Preview lifecycle: PR opened → URL works → push → URL updated → PR closed → everything deleted. Production secret absent in the preview container. | AC-PREV-01, AC-PREV-02, AC-PREV-03, AC-PREV-04 |
| T-E2E-07 | 3 nodes. Join, schedule, move a stateless service without failed requests. Kill node with `failover: auto` and check recovery time. Return the node and check no double run. | AC-NODE-01, AC-NODE-03, AC-NODE-04, AC-NODE-07 |
| T-E2E-08 | Failover with a volume: opt-in off blocks it, opt-in on restores from backup. | AC-NODE-05, AC-NODE-06 |
| T-E2E-09 | Postgres with two targets: backup, restore check passes. Corrupt the snapshot: check fails and alert arrives (fake Telegram server). | AC-BKP-01, AC-BKP-07, AC-BKP-08, AC-ALERT-01 |
| T-E2E-10 | RBAC: four users and a protected production. Matrix test over all operations (generated from `x-permission`). | AC-RBAC-01, AC-RBAC-02, AC-RBAC-03 |
| T-E2E-11 | SSO with a test OIDC provider (group mapping, unverified email, sso_required, break-glass). | AC-SSO-01, AC-SSO-02, AC-SSO-03 |
| T-E2E-12 | Config as code: plan, apply, UI edit blocked, prune rules, export without secret values. Terraform apply and idempotent plan. | AC-CAC-01 to AC-CAC-06, AC-TF-01, AC-TF-02 |
| T-E2E-13 | Control plane down for 10 min: apps keep serving, domains keep working, agent reconnects, state converges. | REQ-CORE-007, REQ-AGENT-005 |
| T-E2E-14 | Audit chain: change a row in the DB, `verify` finds it. KEK rotation with no restart of services. | AC-AUDIT-02, AC-SEC-04 |
| T-E2E-15 | Log search and 60-day metrics range on seeded data. | AC-OBS-05, AC-OBS-06, AC-OBS-07 |

## 5. Test data and doubles

- Fake Git provider (GitHub-compatible webhook sender and API), fake OIDC provider, fake Telegram/Slack/SMTP servers, local ACME CA (Pebble), local S3 (MinIO) and SFTP server for backups.
- Sample apps in `/test/fixtures`: `hello` (HTTP, returns its version), `slowstart` (healthy after N s), `flaky` (5xx on demand), `stateful` (writes to a volume), `ws` (WebSocket echo).

## 6. Quality rules for code written from this spec

- **REQ-CORE-030** (MUST) Code is organized by the module list in [02-architecture](02-architecture.md#5-server-internals). A package for deploy logic has no HTTP code. HTTP handlers have no business logic beyond validation and calling a service.
- **REQ-CORE-031** (MUST) All time-dependent logic (timers, thresholds) uses an injected clock so tests do not sleep.
- **REQ-CORE-032** (MUST) Every external call (Docker, Caddy, Git, ACME, S3, SMTP) is behind an interface with a fake used by tests.
- **REQ-CORE-033** (MUST) Errors carry the stable codes of [05-api](05-api.md#2-standard-error-codes). Panics are caught at the request boundary and reported with `request_id`.
- **REQ-CORE-034** (MUST) Logs of the server and agent are structured JSON, include `request_id` or `command_id`, and never contain secret values.
- **REQ-CORE-035** (SHOULD) Every milestone ends with a tagged pre-release and the milestone's done criteria in [14](14-roadmap-and-milestones.md) are automated in CI as far as possible.
