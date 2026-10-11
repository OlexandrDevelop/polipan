# F04 – Promote staging to production — Gap 4

## Summary

A user promotes a Release from one environment (for example `staging`) to another (for example `production`) with one action. Polipan reuses the same image digest and the same config, and leaves out the settings that belong to the target environment. No rebuild. No copy by hand. A diff is shown before confirm.

## User stories

- **US-PROMO-01** As a developer, I test in staging, then promote exactly that build to production.
- **US-PROMO-02** As a lead, I see a diff of what will change in production before I confirm.
- **US-PROMO-03** As an admin, I know that production domains, secrets and sizes are not overwritten.
- **US-PROMO-04** As a developer, I promote the whole project (all services) or a single service.

## Rules

- **REQ-PROMO-001** (MUST) Promotion creates a new Release in the target service from a source Release: same `image_digest`, same `config_snapshot` **except** the env-specific fields (REQ-PROMO-002). `source.promoted_from_release_id` is set. No build runs.
- **REQ-PROMO-002** (MUST) Env-specific fields, never copied by promotion: `domains`, env-scoped and service-scoped variables and secrets of the target (the target keeps its own), `replicas`, `placement`, `resources` (if the target overrides them), `failover`, `volumes` (names and nodes), backup policies, `deploy` strategy options of the target environment. Everything else (image, command, entrypoint, ports, health check, `pre_deploy`, build-independent config, shared project variables) comes from the source.
- **REQ-PROMO-003** (MUST) Variables resolve in the **target** scope at promotion time. If the source Release used a name that does not exist in the target (neither shared nor target-scoped), the plan lists it as `missing` and the promotion is refused (422 `missing_variables`) until it is set or `allow_missing: ["NAME"]` is passed.
- **REQ-PROMO-004** (MUST) A dry run (`dry_run: true`) returns the plan: source and target Release, image digest, config diff (field, before, after), variables added/removed/missing (names only for secrets), strategy to use, and warnings. The UI shows it as a diff screen. A promotion with `plan_hash` from the dry run fails with 409 `plan_changed` if the target changed in between.
- **REQ-PROMO-005** (MUST) The new Release is deployed with the target environment's strategy (for example canary in production) through the normal deploy engine ([08](../08-deploy-engine.md)). Health gates and rollback apply.
- **REQ-PROMO-006** (MUST) Promotion matches services by **name** between the two environments. A service missing in the target is created from the source config (domains left empty, replicas 1) only if `create_missing: true`; otherwise it is listed and skipped.
- **REQ-PROMO-007** (MUST) Promoting into a protected environment needs `admin` or higher, judged on the target ([10](../10-security.md#3-roles-and-permissions)). The audit event stores source and target ids and the plan hash.
- **REQ-PROMO-008** (SHOULD) Project-wide promote runs services in dependency order (databases first, then apps, based on `depends_on`), one Deployment per service. If one fails, the rest of the batch stops and the services already switched stay (and are shown).
- **REQ-PROMO-009** (MUST) Promotion is only allowed between environments of the same project. Preview environments cannot be a promotion target.
- **REQ-PROMO-010** (MUST) Source Releases must be `available` (image not pruned) and their last Deployment must have been `live` at least once in the source. Otherwise 409 `release_not_promotable`.

## States

A promotion is a Deployment with `trigger = promotion`. States are the deploy states of [08](../08-deploy-engine.md#2-state-machine). The batch (project-wide) is a record `promotion_batches` in the `events` stream with per-service results (not a separate table in v1: results are in the deployments).

The deployments of a batch share `batch_id`. Each one after the first has `after_deployment_id` and waits in the queue until the deployment before it is `live`. If that one ends in another state, the waiting deployment is `cancelled` with the reason `batch_stopped`, and so are the ones that wait for it. The answer of the batch lists one Deployment for each service that is not skipped, in the order of the run.

## Edge cases

- Staging has newer config than the Release being promoted: only the **Release** is promoted. Live (unreleased) config changes are ignored. The UI states this.
- Target has a different port exposed than source: the plan flags `port_changed` and the domain port is checked. It refuses if the target domain points to a port the promoted Release does not expose (`domain_port_mismatch`).
- Secrets exist only in the source: reported as `missing` names (not values).
- Source and target share the same shared variable: fine, one value.
- A database service in the batch: promotion of a `database` service only changes image tag/config. Data is never copied. Databases are skipped by default in project promotion (`include_databases: false`).
- Concurrent deployment in target: queued, like any deploy.

## Touch points

API: `POST /releases/{id}/promote` (`target_environment_id`, `dry_run`, `plan_hash`, `allow_missing`, `create_missing`), `POST /environments/{id}/promote` (batch, `target_environment_id`). CLI: `polipan promote staging production [--service api] [--dry-run]`. Config: `environments.<name>.promote_from` ([07](../07-config-as-code.md)). UI: Promote dialog with diff.

## Acceptance criteria

- **AC-PROMO-01** Given release v5 live in staging, When promoted to production, Then the new production Release has the same `image_digest` and no build job ran. (REQ-PROMO-001)
- **AC-PROMO-02** Given production has its own domain and secrets, When promoted, Then the domains and secrets of production are unchanged. (REQ-PROMO-002)
- **AC-PROMO-03** Given a variable used by the source Release but missing in production, When promoted, Then 422 `missing_variables` listing the names. (REQ-PROMO-003)
- **AC-PROMO-04** Given `dry_run: true`, When called, Then nothing changes and the plan shows the diff. (REQ-PROMO-004)
- **AC-PROMO-05** Given the target environment strategy is canary, When promoted, Then the Deployment runs canary. (REQ-PROMO-005)
- **AC-PROMO-06** Given a developer, When promoting into protected production, Then 403. (REQ-PROMO-007)
- **AC-PROMO-07** Given a project-wide promote where the second service fails, When it fails, Then the third is not started and the result lists the three outcomes. (REQ-PROMO-008)
- **AC-PROMO-08** Given a plan_hash from a dry run and a changed target, When confirming, Then 409 `plan_changed`. (REQ-PROMO-004)

## Out of scope

Copying data between environments. Multi-project promotion. Approval workflows (later, [Q-05](../15-open-questions.md)).
