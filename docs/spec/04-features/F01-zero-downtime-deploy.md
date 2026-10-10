# F01 – Zero-downtime deploy (blue-green and canary) — Gap 1

## Summary

The new version starts next to the old one, in its own set of containers, from an immutable image digest. When health checks pass, traffic switches in one atomic step. For canary, traffic moves in steps with automatic gates. The service stays available during and after the deploy. The exact engine is [08-deploy-engine](../08-deploy-engine.md). This file is the product contract and the honest limits.

## User stories

- **US-DEPLOY-01** As a developer, I deploy and users see no errors and no downtime.
- **US-DEPLOY-02** As a developer, I see the new version start, pass health checks, and take traffic, step by step.
- **US-DEPLOY-03** As a developer, I pick canary for a risky change and watch 10 % of traffic first.
- **US-DEPLOY-04** As a developer, I am told clearly when zero downtime is not possible for a service and why.
- **US-DEPLOY-05** As a developer, I cancel a deploy before the switch and the old version keeps serving.
- **US-DEPLOY-06** As a developer, I run database migrations before the new version takes traffic.

## Rules

- **REQ-DEPLOY-001** (MUST) Strategies: `recreate`, `blue_green`, `canary`. Default `blue_green` for services with a domain or port and no single-writer volume, else `recreate` ([glossary](../01-glossary.md#defaults)).
- **REQ-DEPLOY-002** (MUST) In `blue_green` and `canary` the previous Release keeps serving until the switch is acknowledged. A failure at any earlier step leaves it serving ([REQ-DEPLOY-106](../08-deploy-engine.md#requirements)).
- **REQ-DEPLOY-003** (MUST) The switch happens only after all green replicas pass their health checks ([F05](F05-health-checks.md)).
- **REQ-DEPLOY-004** (MUST) The new version runs from an immutable image digest in its own containers (`<service>-<release_short>-<replica>`). The switch moves ingress from the old containers to the new ones.
- **REQ-DEPLOY-005** (MUST) The strategy can be set per service, per environment kind (for example canary only in production) and per deploy request.
- **REQ-DEPLOY-006** (MUST) Canary runs steps `canary_steps` with `canary_step_interval` and the gates of [08](../08-deploy-engine.md#6-canary). The user can promote, abort or pause.
- **REQ-DEPLOY-007** (MUST) The deploy response and the UI show the deployment plan before start: strategy, why (if forced), expected downtime (none or "yes, about N s"), nodes, and warnings.
- **REQ-DEPLOY-008** (MUST) Forced `recreate` cases (single-writer volume, database type, no ingress, host port) are documented in the UI next to the strategy field. Zero downtime is NOT promised for them.
- **REQ-DEPLOY-009** (MUST) Old containers are drained for `drain_seconds`, stopped with `stop_grace_period`, and kept stopped (`retain_stopped`) for fast rollback ([F02](F02-rollback-and-releases.md)).
- **REQ-DEPLOY-010** (MUST) Resource check: before starting green, the scheduler checks that the node has capacity for blue and green at the same time. If not, the deploy fails early with `node_unavailable` (or, only if the user set `allow_downtime_on_low_capacity: true`, falls back to `recreate` with a warning).
- **REQ-DEPLOY-011** (SHOULD) Deploy hooks: `pre_deploy` (before green) and `post_deploy` (after live, failure only warns).
- **REQ-DEPLOY-012** (MUST) Concurrency, supersede and cancel rules follow [08](../08-deploy-engine.md#10-concurrency-and-queue).

## States

Deployment states: see [08-deploy-engine](../08-deploy-engine.md#2-state-machine). Short path for blue-green: `queued → building → image_ready → starting → health_checking → switching → draining → live`.

## Edge cases

- **Database migrations:** the old and new code run together for a moment. Migrations MUST be backward compatible (expand/contract). Polipan shows a warning on services with `pre_deploy`. It does not check the migration.
- **Long-lived connections** (WebSocket, SSE, streams): Caddy keeps existing connections to blue until they close or `drain_seconds` ends, then they are closed. The client reconnects to green. Set `drain_seconds` higher for long streams.
- **In-memory sessions:** blue and green do not share memory. Apps with in-process sessions need a shared store (the UI hint says so).
- **Cron or worker overlap:** a `worker` runs twice during the overlap unless `allow_overlap` is false (then `recreate`).
- **Singleton services** (a message consumer that must not run twice): use `recreate`.
- **Port conflicts:** blue and green use container IPs, so the same container port is fine. Host ports force `recreate`.
- **Node lacks capacity for two copies:** see REQ-DEPLOY-010.
- **Canary with low traffic:** the gate passes on health only and extends once ([08](../08-deploy-engine.md#6-canary)).
- **Health never passes:** fails at `deploy_timeout`, green is removed after `failed_green_keep`, blue untouched.
- **Ingress node down during switch:** skipped, gets the config on return.
- **Single-replica service:** still zero downtime, since blue and green are separate containers.

## Touch points

API: `POST /services/{id}/deploy` (body: `strategy`, `strategy_options`, `commit`), `/deployments/{id}`, `/deployments/{id}/cancel|promote|abort|pause|resume`, `/deployments/{id}/events`. CLI: `polipan deploy`, `polipan deploy --strategy canary`, `polipan deployment promote|abort`. Config: `services.<name>.deploy`. UI: Deploy dialog, Deployment page with live steps ([11-ui](../11-ui.md)).

## Acceptance criteria

- **AC-DEPLOY-01** Given a stateless app with domain and a client sending 20 requests per second, When a new Release is deployed with `blue_green`, Then no request fails and the response version changes from old to new at one moment. (REQ-DEPLOY-001, REQ-DEPLOY-002, REQ-DEPLOY-004)
- **AC-DEPLOY-02** Given green never becomes healthy, When `deploy_timeout` passes, Then the deployment is `failed` with `health_timeout`, blue still serves, and no request failed. (REQ-DEPLOY-002, REQ-DEPLOY-003)
- **AC-DEPLOY-03** Given a canary deploy, When a step gate fails (green 5xx above threshold), Then traffic returns to 100 % blue and the state is `rolled_back` with `gate_5xx`. (REQ-DEPLOY-006)
- **AC-DEPLOY-04** Given a service with a single-writer volume, When the user requests `blue_green`, Then the plan shows `recreate`, expected downtime, and the warning. (REQ-DEPLOY-007, REQ-DEPLOY-008)
- **AC-DEPLOY-05** Given a node with capacity for one copy only, When a blue-green deploy starts, Then it fails with `node_unavailable` before any container starts. (REQ-DEPLOY-010)
- **AC-DEPLOY-06** Given a deploy in `health_checking`, When the user cancels, Then green is removed and blue serves. (REQ-DEPLOY-012)
- **AC-DEPLOY-07** Given a successful blue-green, When it finishes, Then blue containers are stopped, not removed, and `retain_stopped` sets exist. (REQ-DEPLOY-009)
- **AC-DEPLOY-08** Given `pre_deploy` exits with code 1, When deployed, Then green never starts and the state is `failed` with `pre_deploy_failed`. (REQ-DEPLOY-011)
- **AC-DEPLOY-09** Given an app with an open WebSocket, When the switch is done, Then the WebSocket is closed only after `drain_seconds`. (REQ-DEPLOY-009)

## Out of scope

Sticky canary by user or cookie, header-based routing (planned later), shadow traffic, automatic database migration safety checks.
