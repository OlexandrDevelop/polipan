# Easypanel Parity Matrix

<a id="requirements"></a>

- **REQ-UI-024** (MUST) Every Easypanel feature in the first table, and every Polipan extra in the second table, is covered by at least one screen of [screens](screens.md). A CI check reads both tables, extracts the `UI-NN` ids and fails if an id does not exist in `screens.md`, or a row has no id.

Each row was checked against [00-vision-and-scope](../00-vision-and-scope.md) and the feature files C01–C08. "Gap" is always "covered": no row is left open. Where Polipan does the job in a different way, the row says so.

## 1. Easypanel features

| Easypanel feature | Polipan screens | Spec source | Note |
|---|---|---|---|
| Projects | UI-04, UI-33 | [C01](../04-features/C01-apps-git-and-image.md) | Plus environments inside a project. |
| Apps from Git (GitHub and others) | UI-05, UI-32, UI-27, UI-06 | C01 | GitHub App, GitLab, Gitea, deploy key. |
| Apps from Dockerfile | UI-32, UI-06 | C01 | Builder `dockerfile`. |
| Apps with Nixpacks | UI-32, UI-06 | C01 | Polipan uses Railpack (builder `railpack`), the successor of Nixpacks. |
| Apps from a Docker image | UI-32, UI-06 | C01 | Registry login supported. |
| Databases: Postgres, MySQL, MariaDB, MongoDB, Redis | UI-32 (database path), UI-06, UI-17 | [C02](../04-features/C02-databases.md) | Credentials shown once, backups in UI-17. |
| Templates, one-click apps | UI-21, UI-32 | [C03](../04-features/C03-templates.md) | Custom templates for D, A, O. |
| Docker Compose import | UI-05, UI-32 | C03 (`importCompose`) | Import creates services; it is not a runtime Compose engine. |
| Domains, automatic HTTPS, redirects, wildcard | UI-12, UI-28 | [C04](../04-features/C04-domains-https.md) | DNS records with copy buttons. |
| Environment variables and build arguments | UI-11 | [C05](../04-features/C05-env-vars.md) | Build-time flag per variable. |
| Secrets and shared variables | UI-11, UI-49 | C05, [10](../10-security.md#4-secrets) | Org, project, environment scopes; versions. |
| Volumes and bind mounts, file mounts | UI-13 | [C06](../04-features/C06-volumes.md) | Orphan volumes list. |
| Ports and proxy settings | UI-06 (settings), UI-12 | C01, C04 | Caddy replaces Traefik; settings are per service and domain. |
| Resource limits (CPU, RAM) and replicas | UI-06 (settings) | C01 | Replicas place across nodes. |
| Deploy hooks, webhooks, auto deploy on push | UI-06 (settings), UI-27 | C01 | `receiveGitWebhook`, `receiveDeployHook`. |
| Cron jobs and scheduled jobs | UI-06 (service type `cron`, schedule and last runs) | [glossary](../01-glossary.md) service type `cron`, [03](../03-data-model.md) | Covered: `cron` and `worker` are service types, so no gap. |
| Live logs | UI-15 | [C07](../04-features/C07-logs-metrics-basic.md) | |
| Monitoring: CPU, memory, network, disk | UI-16, UI-40 | C07 | Service charts and node charts. |
| Terminal and console | UI-30 | C01 (US-APP-06) | Audited. Desktop and tablet. |
| Backups of databases and volumes, S3 targets | UI-17, UI-38, UI-39 | [C08](../04-features/C08-backups-basic.md), [F10](../04-features/F10-backups-and-restore-checks.md) | |
| Server settings, Docker cleanup, proxy settings | UI-40, UI-28 | [09](../09-multi-server.md), F02 | Cleanup of images is automatic; orphan volumes are listed and removed in UI-40. No manual "docker prune" button: this is by design. |
| Users and team (basic) | UI-23, UI-45 | [F07](../04-features/F07-teams-rbac-audit-sso-secrets.md) | |
| API and access tokens | UI-25 | [05](../05-api.md) | |
| Dark and light UI, mobile use | UI-43, [design-system](design-system.md), [responsive](responsive.md) | [REQ-UI-014](design-system.md#requirements), [REQ-UI-016](responsive.md#requirements) | |
| Updates of the panel | UI-28 (version and update notice) | [F11](../04-features/F11-open-source.md) | `getVersion`. The update notice shows only when the opt-in update check is on ([REQ-OSS-005](../04-features/F11-open-source.md#rules)). The update itself runs from the installer or CLI, not from the browser. |

## 2. Polipan extra features

| Extra feature | Polipan screens | Spec source |
|---|---|---|
| Blue-green and canary deploy, traffic switch | UI-07, UI-08 | [F01](../04-features/F01-zero-downtime-deploy.md) |
| Health checks before the switch | UI-14, UI-08 | [F05](../04-features/F05-health-checks.md) |
| One-click rollback and release history | UI-09, UI-35, UI-08 | [F02](../04-features/F02-rollback-and-releases.md) |
| Preview environments per pull request | UI-18, UI-36 | [F03](../04-features/F03-preview-environments.md) |
| Promote staging to production with diff | UI-10, UI-33 | [F04](../04-features/F04-promotion.md) |
| Many servers, node actions, join | UI-19, UI-31, UI-40 | [F06](../04-features/F06-multi-server-failover.md) |
| Move service and failover | UI-20, UI-41, UI-40 | F06 |
| Teams, roles, project overrides | UI-23, UI-44, UI-45 | F07 |
| Audit log with chain check | UI-26 | F07 |
| SSO (OIDC, Google, GitHub) | UI-24, UI-01 | F07 |
| Secrets with versions, write-only | UI-11, UI-49 | F07, C05 |
| Alerts and channels | UI-22, UI-48, UI-46 | [F09](../04-features/F09-alerts-metrics-logs.md) |
| Searchable logs | UI-37, UI-15 | F09 |
| Long metrics (90 days) | UI-16, UI-40 | F09 |
| Backups with restore check and many targets | UI-17, UI-38, UI-39 | F10 |
| Config as code, export, plan, drift | UI-29 | [F08](../04-features/F08-config-as-code-api-cli-terraform.md) |
| Open source, no lock-in (version, links) | UI-28 | F11 |

## 3. Checks

1. Every id above exists in [screens](screens.md#2-index).
2. Every UI screen id `UI-01`..`UI-49` appears in a row of this file, in the navigation of [navigation](navigation.md) or in the entry list of [screens](screens.md). Screens that only support others (UI-42, UI-46, UI-47) are listed in the "Supporting screens" line.

Supporting screens: UI-02 (setup), UI-03 (dashboard), UI-05 (chooser), UI-34 (deployments list), UI-42 (palette), UI-46 (bell), UI-47 (errors).
