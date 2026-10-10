# Polipan Specification

Polipan is a self-hosted, open source server panel. It does what Easypanel does (apps from Git or Docker image, databases, templates, domains, automatic HTTPS, variables and secrets, volumes, logs, metrics, backups, web UI) and fixes the gaps listed in [00-vision-and-scope](00-vision-and-scope.md).

This specification is written for **AI build agents**. It is complete and exact: the tech stack is fixed, names and numbers are in one place, every requirement has an id, and every feature has acceptance criteria. It contains no application code, only schemas, API shapes and short examples.

## How to use this spec (rules for build agents)

1. Read the files in the **reading order** below before you write code.
2. Take a milestone from [14-roadmap-and-milestones](14-roadmap-and-milestones.md). Build only what its `Covers` line lists. Check your work against its done criteria.
3. For each `REQ-*` you build, read its feature file. The acceptance criteria (`AC-*`) there are your tests. Name each test with the AC id.
4. Names, enums and default numbers come from [01-glossary](01-glossary.md). Never invent a different value. If a value is missing, add it to the glossary first.
5. If the spec is unclear or two files disagree, do not guess. Write the question in your task, and prefer the order of authority below.
6. Do not change the stack. Product questions are in [15-open-questions](15-open-questions.md), each with a default to use now.

**Order of authority** when two files disagree: `01-glossary` (names and defaults) → `openapi.yaml`, `schemas/agent.proto`, `schemas/*.json` (machine formats) → `03-data-model` → `08-deploy-engine` and `09-multi-server` → `10-security` → feature files `04-features/*` → the rest.

## Reading order

| # | File | Read it to learn | Needed by |
|---|---|---|---|
| 1 | [00-vision-and-scope](00-vision-and-scope.md) | Goals, non-goals, honest limits | everyone |
| 2 | [01-glossary](01-glossary.md) | Terms, **defaults table**, enums, id prefixes | everyone |
| 3 | [02-architecture](02-architecture.md) | Components, **fixed tech stack and reasons**, repo layout, flows, ports | everyone |
| 4 | [03-data-model](03-data-model.md) | Entities, ER diagram, PostgreSQL DDL, rules | server |
| 5 | [08-deploy-engine](08-deploy-engine.md) | Deployment state machine, blue-green, canary, recreate, rollback | server, agent |
| 6 | [09-multi-server](09-multi-server.md) + [schemas/agent.proto](schemas/agent.proto) | Agent protocol, join, desired state, scheduling, failover | server, agent |
| 7 | [10-security](10-security.md) | Auth, **permission matrix**, secrets crypto, audit, threat model | everyone |
| 8 | [04-features/](04-features/) | One file per feature with stories, rules, states, edge cases, acceptance criteria | the track you build |
| 9 | [05-api](05-api.md) + [openapi.yaml](openapi.yaml) | API conventions and every endpoint | server, web, CLI |
| 10 | [06-cli](06-cli.md) | CLI commands | CLI |
| 11 | [07-config-as-code](07-config-as-code.md) + [schemas/polipan.schema.json](schemas/polipan.schema.json) + [schemas/template.schema.json](schemas/template.schema.json) | `polipan.yaml` format, plan/apply | server, CLI, Terraform |
| 12 | [11-ui](11-ui.md) + [ui/](ui/README.md) | Screens and flows; full UI/UX design: tokens, responsive rules, navigation, screen inventory, Easypanel parity matrix, components, accessibility, tests, wireframes | web |
| 13 | [12-observability](12-observability.md) | Metrics, logs, events, alert engine | agent, server |
| 14 | [13-testing-and-quality](13-testing-and-quality.md) | Test levels, CI gates, E2E scenarios | everyone |
| 15 | [14-roadmap-and-milestones](14-roadmap-and-milestones.md) | Ordered milestones, dependencies, done criteria | everyone |
| 16 | [traceability](traceability.md) | REQ → file → milestone → tests | planning, review |
| 17 | [15-open-questions](15-open-questions.md) | Open product questions with defaults | the user |

Minimal reading for a single work package: 01, 02, the feature file, the milestone section, and the sections it links (data model tables, API operations).

## Feature index

Core features (what Easypanel already does):

| Id | File | Area |
|---|---|---|
| C01 | [Apps from Git and image](04-features/C01-apps-git-and-image.md) | APP |
| C02 | [Databases](04-features/C02-databases.md) | DB |
| C03 | [Templates](04-features/C03-templates.md) | TPL |
| C04 | [Domains and HTTPS](04-features/C04-domains-https.md) | DOM |
| C05 | [Variables and secrets](04-features/C05-env-vars.md) | ENV |
| C06 | [Volumes](04-features/C06-volumes.md) | VOL |
| C07 | [Logs and metrics (basic)](04-features/C07-logs-metrics-basic.md) | OBS |
| C08 | [Backups (basic, delivered by F10)](04-features/C08-backups-basic.md) | BKP |

Gap features (what Polipan adds):

| Id | Gap | File | Area |
|---|---|---|---|
| F01 | 1. Zero-downtime deploy (blue-green, canary) | [F01](04-features/F01-zero-downtime-deploy.md) | DEPLOY |
| F02 | 2. One-click rollback, release history | [F02](04-features/F02-rollback-and-releases.md) | REL |
| F03 | 3. Preview environments per PR | [F03](04-features/F03-preview-environments.md) | PREV |
| F04 | 4. Promote staging to production | [F04](04-features/F04-promotion.md) | PROMO |
| F05 | 5. Health checks before switch | [F05](04-features/F05-health-checks.md) | HC |
| F06 | 6. Many servers, failover, move | [F06](04-features/F06-multi-server-failover.md) | NODE |
| F07 | 7. Teams, roles, audit, SSO, secrets | [F07](04-features/F07-teams-rbac-audit-sso-secrets.md) | AUTH, RBAC, AUDIT, SSO, SEC |
| F08 | 8. Config as code, API, CLI, Terraform | [F08](04-features/F08-config-as-code-api-cli-terraform.md) | CAC, API, CLI, TF |
| F09 | 9. Alerts, long metrics, searchable logs | [F09](04-features/F09-alerts-metrics-logs.md) | ALERT, OBS |
| F10 | 10. Backups, restore checks, many targets | [F10](04-features/F10-backups-and-restore-checks.md) | BKP |
| F11 | 11. Open source, no lock-in | [F11](04-features/F11-open-source.md) | OSS |

## Fixed stack (summary)

Go server, agent, CLI and Terraform provider · React + TypeScript SPA embedded in the server · PostgreSQL 16 with a Postgres job queue · plain Docker Engine scheduled by Polipan · Caddy 2 on every node (atomic `/load` = instant switch) · agent dials out over gRPC + mTLS · WireGuard mesh · VictoriaMetrics (90 d) and VictoriaLogs (30 d) · BuildKit + Railpack · built-in OCI registry · restic · OpenAPI 3.1 · Apache-2.0. Reasons: [02-architecture](02-architecture.md#2-tech-stack-and-reasons).

## ID rules

| Id | Form | Rule |
|---|---|---|
| Requirement | `REQ-<AREA>-<NNN>` | Defined once, as a list item `- **REQ-AREA-NNN** (MUST\|SHOULD\|MAY) text`. Testable. Numbers are never reused. A dropped requirement stays with the text "withdrawn". |
| User story | `US-<AREA>-<NN>` | In the feature file. |
| Acceptance criterion | `AC-<AREA>-<NN>` | Given/When/Then, lists the REQ ids it checks. It is a test scenario. The test name contains the id. |
| Cross-feature test | `T-E2E-<NN>` | Listed in [13](13-testing-and-quality.md#4-cross-feature-e2e-scenarios). |
| Milestone | `M<n>` | In [14](14-roadmap-and-milestones.md). |
| Open question | `Q-<NN>` | In [15](15-open-questions.md). |
| Screen | `UI-<NN>` | In [11](11-ui.md) (UI-01 to UI-30) and [ui/screens](ui/screens.md) (all). |
| Work package | `WP<m>.<n>` | In [14](14-roadmap-and-milestones.md). |

**Areas** (fixed list): `CORE` platform and quality · `APP` apps · `DB` databases · `TPL` templates · `DOM` domains · `ENV` variables · `VOL` volumes · `DEPLOY` deploy engine · `REL` releases · `PREV` previews · `PROMO` promotion · `HC` health checks · `NODE` nodes · `AGENT` agent protocol · `AUTH` authentication · `RBAC` roles · `AUDIT` audit · `SSO` single sign-on · `SEC` secrets and security rules · `CAC` config as code · `API` · `CLI` · `TF` Terraform · `ALERT` · `OBS` observability · `BKP` backups · `OSS` open source · `UI`.

Number ranges: a feature file uses `001`–`099`. The technical files use `100` and up for the same area (for example `REQ-DEPLOY-001` in F01, `REQ-DEPLOY-100` in the deploy engine file). Exceptions are noted in the file.

Levels follow RFC 2119: **MUST** is required for the milestone, **SHOULD** is expected unless there is a written reason, **MAY** is optional.

How to cite: in code comments, tests, commits and tasks write the id, for example `REQ-DEPLOY-102` or `AC-REL-02`.

## Consistency checks

These checks were run on this spec. Run them again after any change. They need only `grep` and a YAML/JSON parser.

1. **Unique ids.** Every `REQ-*` definition (list item that starts with `- **REQ-`) appears once in `docs/spec/**/*.md` (not counting `traceability.md`). Same for `AC-*`.
2. **No dangling references.** Every `REQ-*` mentioned anywhere is defined.
3. **Traceability complete.** Every defined `REQ-*` is in [traceability](traceability.md) with a milestone, and in exactly one `Covers` line of the roadmap.
4. **Links resolve.** Every relative Markdown link points to an existing file, and `#anchor` parts match a heading.
5. **Defaults.** Default numbers (drain 30 s, heartbeat 5/15/60 s, canary steps, retention, thresholds) appear with the same value everywhere. The master list is [01-glossary](01-glossary.md#defaults).
6. **No program code.** Code fences use only these languages: `mermaid`, `yaml`, `json`, `sql` (schema DDL only: CREATE TABLE, CREATE INDEX, ALTER TABLE), `text`. No Go, TypeScript, JavaScript, Python or shell code fences exist.
7. **Machine files valid.** `openapi.yaml` is valid OpenAPI 3.1 and every operation has `operationId` and `x-permission` from the matrix of [10-security](10-security.md#permission-matrix). `schemas/polipan.schema.json` and `template.schema.json` are valid JSON Schema 2020-12, and the examples of [07](07-config-as-code.md#8-examples) validate against `polipan.schema.json`. `schemas/agent.proto` is proto3.
8. **Milestone references.** Every `AC-*` or `T-E2E-*` named in a milestone's done criteria exists.
