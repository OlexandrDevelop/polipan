# 00 – Vision and Scope

## Vision

Polipan is a self-hosted, open source server panel. A user adds one or many servers, connects a Git repository or a Docker image, and Polipan builds, deploys, secures (HTTPS), monitors and backs it up. It does what Easypanel does and fixes the gaps listed below.

## Personas

| Persona | Need |
|---|---|
| Solo developer | Deploy from Git with HTTPS in minutes. No downtime on deploy. One-click rollback. |
| Small team lead | Roles, audit log, SSO, staging → production promotion, preview per pull request. |
| Platform engineer | Many servers, failover, config as code, API, CLI, Terraform, alerts, backups with restore checks. |
| Build agent (AI) | Reads this spec and builds the product alone. Needs exact names, ids and checkable criteria. |

## Goals (the gaps, each is a first-class feature)

| # | Gap | Feature file | Area |
|---|---|---|---|
| 1 | Zero-downtime deploy (blue-green, canary) | [F01](04-features/F01-zero-downtime-deploy.md) | DEPLOY |
| 2 | One-click rollback and release history | [F02](04-features/F02-rollback-and-releases.md) | REL |
| 3 | Preview environments per pull request | [F03](04-features/F03-preview-environments.md) | PREV |
| 4 | Promote staging to production | [F04](04-features/F04-promotion.md) | PROMO |
| 5 | Health checks before traffic switch | [F05](04-features/F05-health-checks.md) | HC |
| 6 | Many servers, failover, move service | [F06](04-features/F06-multi-server-failover.md) | NODE |
| 7 | Teams, roles, audit, SSO, secrets | [F07](04-features/F07-teams-rbac-audit-sso-secrets.md) | RBAC, AUDIT, SSO, SEC, AUTH |
| 8 | Config as code, API, CLI, Terraform | [F08](04-features/F08-config-as-code-api-cli-terraform.md) | CAC, API, CLI, TF |
| 9 | Alerts, long metrics, searchable logs | [F09](04-features/F09-alerts-metrics-logs.md) | ALERT, OBS |
| 10 | Backups with restore checks, many targets | [F10](04-features/F10-backups-and-restore-checks.md) | BKP |
| 11 | Open source, no lock-in | [F11](04-features/F11-open-source.md) | OSS |

Core features (what Easypanel already does) are in `04-features/C01`–`C08`.

## Non-goals for v1

- Kubernetes or Docker Swarm. Polipan schedules plain Docker itself.
- High availability of the control plane. One control plane. When it is down, running apps keep running (agents and Caddy keep the last config), but no deploy, UI or API works.
- Windows nodes. Billing. A hosted SaaS offer. A marketplace of paid templates.
- Automatic DNS record management (see [Q-03](15-open-questions.md)).
- External secret backends such as Vault/OpenBao (milestone after 1.0, see [roadmap](14-roadmap-and-milestones.md)).

## Honest limits (stated up front)

- Zero downtime is promised only for services that can run two copies at once. A service with a single-writer volume (for example a database) uses `recreate` and has a short downtime on deploy. See [F01](04-features/F01-zero-downtime-deploy.md).
- Blue-green needs database migrations that work with both old and new code. Polipan cannot check this.
- Failover of stateful services can lose data written after the last backup. It needs an explicit opt-in.

## Comparison with other panels

| Capability | Easypanel | Coolify | Dokploy | CapRover | Polipan (target) |
|---|---|---|---|---|---|
| Git / image apps, DBs, templates | yes | yes | yes | yes | yes |
| Automatic HTTPS | yes | yes | yes | yes | yes |
| Zero-downtime blue-green | no (rolling stop/start) | limited | limited (Swarm rolling) | no | yes, health-gated instant switch |
| Canary | no | no | no | no | yes |
| One-click rollback | limited | yes (needs image kept) | yes | limited | yes, instant when old copy is kept |
| Preview per PR | limited | yes | yes | no | yes, with secret isolation |
| Promote staging → prod | no | no | no | no | yes, no rebuild |
| Multi-server + failover | no | multi-server, no failover | Swarm | Swarm | yes, mesh + auto failover |
| RBAC, audit log, SSO | limited | teams | limited | no | yes, hash-chained audit |
| Config as code, Terraform | no | limited | limited | no | yes, `polipan.yaml` + provider |
| Alerts, long metrics, log search | basic | basic | basic | no | yes |
| Backup restore check | no | no | no | no | yes, automatic |
| Open source | no (closed core) | yes | yes | yes | yes, Apache-2.0 |

The table is a planning aid based on public docs at the time of writing. It is not a promise about other products.

## Success measures for 1.0

- A new user goes from empty server to HTTPS app from Git in under 10 minutes.
- Blue-green deploy of a stateless app shows zero failed requests in the e2e test ([T-E2E-03](13-testing-and-quality.md#4-cross-feature-e2e-scenarios)).
- Fast rollback reaches the `fast_rollback_target` in [defaults](01-glossary.md#defaults).
- Every REQ in [traceability](traceability.md) has a test scenario.

## Requirements

- **REQ-CORE-001** (MUST) Polipan is released under the Apache-2.0 license, subject to [Q-01](15-open-questions.md).
- **REQ-CORE-002** (MUST) The non-goals above are not built in v1.
- **REQ-CORE-003** (MUST) Every gap in the table above has its own feature file and is delivered by 1.0 (see [roadmap](14-roadmap-and-milestones.md)).
