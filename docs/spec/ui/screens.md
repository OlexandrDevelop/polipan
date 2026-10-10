# Screen Inventory

This file is the contract for every screen. Ids `UI-01`..`UI-30` are the ids of [11-ui](../11-ui.md#2-screens) and never change. `UI-31`..`UI-49` are new. All operation names are `operationId` values of [openapi.yaml](../openapi.yaml). Method, path and permission of each are in [api-map](api-map.md).

## 1. Conventions used by every entry

### 1.1 Roles and permission behavior

Roles: **O** owner, **A** admin, **D** developer, **V** viewer. `P` means allowed for D only on a non-protected environment. The permission names are the `x-permission` values in the [matrix](../10-security.md#permission-matrix). The UI gets the role from `getMe` (`org_role`, `project_roles`) and decides with a permission table generated from that matrix. The server still checks every call.

| Case | UI behavior (REQ-UI-004) |
|---|---|
| The role can never do the action (for example V and Deploy) | The control is **hidden**. |
| The role can do it in general but the state blocks it (service is building, node is down) | **Disabled** with a tooltip that says why. |
| The role has `P` and the environment is protected | **Disabled** with the tooltip "Needs admin on a protected environment". An admin sees it enabled. |
| The user cannot read the object at all | UI-47 (403). |
| The server answers 403 although the UI allowed it (stale role) | Banner "Your role changed. Reload to see what you can do", with a button that calls `getMe`. |

### 1.2 States that every screen has

<a id="requirements"></a>

- **REQ-UI-022** (MUST) Every screen has these states, each built with the shared components: **empty** (explains the next step, one action), **loading** (skeleton in the shape of the content, no spinner-only page), **error** (banner with the reason and a Retry button; the rest of the page stays), **partial** (one panel failed: only that panel shows the error), and **live-disconnected** (when SSE is down for more than 5 s: a banner "Live updates paused. Reconnecting…", data stays visible and is marked stale; on reconnect the screen re-reads and the banner goes). A screen entry below lists only what is special; "standard" means exactly these five.
- **REQ-UI-023** (MUST) Every screen entry lists the operations it uses, by `operationId` that exists in `openapi.yaml`, and its permission behavior (hide or disable). A CI check reads the **Data** and **Actions** lines of [screens](screens.md), extracts every `operationId` in backticks and fails if one is missing from the OpenAPI file. The [known gaps](#known-api-gaps) table is not checked.
- **REQ-UI-025** (MUST) First run is guided: after setup the user sees an onboarding checklist (name the organization, add a server with a copyable command and a live "waiting → connected" row, add a domain with a DNS check, create the first service). Each step can be skipped and resumed from the dashboard.
- **REQ-UI-033** (SHOULD) The restore-verified badge is green when the last restore check passed less than 30 days ago, yellow when older, red when the last check failed or no check ever ran. The badge always has an icon and the text "Restore verified N days ago", "Restore check failed" or "Never verified".

### 1.3 Entry format

Each entry has: **Purpose**, **Data** (read operations), **Actions** (write operations with permission), **States** (beyond the standard five), **Roles**, **Layout** (phone, tablet, laptop, large) and the **Milestone** that builds it ([14](../14-roadmap-and-milestones.md)). Live data comes from `streamEvents` unless another stream is named. Mutating calls send an `Idempotency-Key` where the API allows it.

## 2. Index

| Id | Screen | M | Id | Screen | M |
|---|---|---|---|---|---|
| UI-01 | Login | M1 | UI-26 | Settings: Audit | M5 |
| UI-02 | First setup | M1 | UI-27 | Settings: Git connections | M10 |
| UI-03 | Dashboard | M1 | UI-28 | Settings: Organization | M10 |
| UI-04 | Project page | M1 | UI-29 | Config as code | M6 |
| UI-05 | Add service (chooser) | M2 | UI-30 | Terminal | M2 |
| UI-06 | Service overview | M3 | UI-31 | Onboarding and first server join | M3, M7 |
| UI-07 | Deploy dialog | M3 | UI-32 | Service wizard | M2, M4 |
| UI-08 | Deployment page | M3, M8 | UI-33 | Environments list and create | M1 |
| UI-09 | Releases tab | M3 | UI-34 | Deployments list | M3 |
| UI-10 | Promote dialog | M9 | UI-35 | Release diff | M3 |
| UI-11 | Variables and secrets | M2 | UI-36 | Preview detail | M9 |
| UI-12 | Domains | M1 | UI-37 | Logs search | M8 |
| UI-13 | Storage | M2 | UI-38 | Backup targets | M4 |
| UI-14 | Health check settings | M3 | UI-39 | Restore wizard and result | M4 |
| UI-15 | Logs | M8 | UI-40 | Node detail | M7 |
| UI-16 | Metrics | M8 | UI-41 | Failover and move history | M7 |
| UI-17 | Backups | M4 | UI-42 | Palette and shortcut help | M2 |
| UI-18 | Previews | M9 | UI-43 | Profile, sessions, 2FA, theme | M1, M5 |
| UI-19 | Nodes | M7 | UI-44 | Teams | M5 |
| UI-20 | Move service | M7 | UI-45 | Org members | M5 |
| UI-21 | Catalog | M4 | UI-46 | Notifications bell | M3, M8 |
| UI-22 | Alerts | M8 | UI-47 | Error pages | M1 |
| UI-23 | Settings: Members and Teams | M5 | UI-48 | Alert channels and rules editor | M8 |
| UI-24 | Settings: SSO | M10 | UI-49 | Secret versions panel | M2 |
| UI-25 | Settings: Tokens | M5 | | | |

UI-23 is the settings page that hosts UI-44 and UI-45 as its two tabs.

## 3. Screens UI-01 to UI-30

### UI-01 Login
- **Purpose:** sign in.
- **Data:** `listLoginProviders`.
- **Actions:** `login` (email, password), TOTP step with `verifyTotp`, `startSsoLogin` (button per provider), `requestPasswordReset`, `confirmPasswordReset`, `acceptInvitation` (invite link).
- **States:** error "Wrong email or password" (same text for both, no hint which); 2FA step; locked-out message with wait time; SSO-required org hides the password form (a link "Use a recovery login" appears only if break-glass is on).
- **Roles:** public.
- **Layout:** card, full width on phone, 400 px centered above. Theme follows system.

### UI-02 First setup
- **Purpose:** create the first owner from the one-time URL.
- **Data:** none (the token is in the URL).
- **Actions:** `setupFirstUser`.
- **States:** invalid or used link → UI-47 with text "This setup link was already used."; password rule shown live (12+ characters).
- **Roles:** public.
- **Layout:** as UI-01. After success go to UI-31.

### UI-03 Dashboard
- **Purpose:** what needs attention now.
- **Data:** `listProjects`, `listServices` (per environment, lazy), `listAlertInstances`, `listNodes`, `listRestoreChecks`, `streamEvents`.
- **Actions:** open items; "Resume onboarding"; "Add service" (`service:create`).
- **Content:** 4 summary cards (services by state, firing alerts, nodes by state, needs attention), recent deployments, node health strip, warnings "no backup" and "restore not verified" ([REQ-UI-033](#requirements)).
- **States:** empty (no project) = onboarding checklist (UI-31). Partial per card.
- **Roles:** all read. "Add service" hidden for V.
- **Layout:** see [responsive](responsive.md#3-layout-of-every-main-screen).

### UI-04 Project page
- **Purpose:** the services of one project, by environment.
- **Data:** `getProject`, `listEnvironments`, `listServices`.
- **Actions:** Add service (`service:create`, P), create environment (UI-33), `updateProject`, `deleteProject` (typed name, `project:delete`).
- **States:** empty environment → "Add your first service" ([REQ-UI-010](../11-ui.md#4-rules-and-requirements)).
- **Roles:** read all; D has P on non-protected environments; project delete A/O.
- **Layout:** environment tabs (select on phone), service cards 1/2/3/4 per row. Production tab red-bordered.

### UI-05 Add service (chooser)
- **Purpose:** choose how to create a service. It is the first step of UI-32.
- **Data:** `listTemplates`.
- **Actions:** pick Git, Image, Template, Database, Compose import (`importCompose`).
- **Roles:** `service:create` (P). Hidden for V.
- **Layout:** 1 column of big cards on phone; 2×2 grid above.

### UI-06 Service overview and settings
- **Purpose:** state and control of one service. Hosts the tabs.
- **Data:** `getService`, `listServiceDeployments`, `streamEvents`. Replicas show node, health, state.
- **Actions:** Deploy (UI-07), Roll back (UI-09), `restartService`, `stopService`, `startService`, `moveService` (UI-20), `updateService` (settings: source, build, resources, replicas, ports, health, deploy strategy, placement, failover, hooks, schedule for `cron` services), `deleteService` (typed name).
- **States:** pending changes banner; service type `cron` shows schedule and last runs; `worker` has no domain tab; protected banner "Production. Deploys need admin." when P applies; fields managed by git are read-only with "managed by polipan.yaml" and a link ([REQ-UI-006](../11-ui.md#4-rules-and-requirements)).
- **Roles:** read all; deploy/update `P`; restart D (also on protected); delete P.
- **Layout:** header (name, state badge, environment badge, Deploy primary, Roll back, ⋯ menu); tabs; Overview cards.

### UI-07 Deploy dialog
- **Purpose:** plan before deploy ([REQ-UI-007](../11-ui.md#4-rules-and-requirements)).
- **Data:** `deployService` with `dry_run` (plan).
- **Actions:** `deployService` (confirm). Shows commit (default branch head), strategy that will run with the reason if forced, downtime yes/no, canary steps if chosen, warnings.
- **States:** plan loading skeleton; plan error blocks the button and shows the reason; protected: banner and the button "Deploy to production".
- **Roles:** `service:deploy` (P). V sees no button.
- **Layout:** bottom sheet on phone, dialog above.

### UI-08 Deployment page
- **Purpose:** watch one deployment, control canary, cancel, roll back.
- **Data:** `getDeployment`, `streamDeploymentEvents`, `getDeploymentLogs`.
- **Actions:** `promoteCanary`, `pauseCanary`, `resumeCanary`, `abortCanary`, `cancelDeployment`, `rollbackService`.
- **Content:** step timeline (states of the [glossary](../01-glossary.md#fixed-enumerations)), health per replica, traffic bar (blue and green or canary percent) with gate values, build log (collapsible), after `live` a "Roll back" shortcut for 10 min.
- **States:** terminal states `failed`, `rolled_back`, `cancelled`, `superseded` show the reason and the next step; disconnected banner is critical here: buttons stay enabled but ask the server for the current state first.
- **Roles:** controls need `service:deploy` (P); cancel needs `deployment:cancel` (P). V sees the page read-only.
- **Layout:** phone one column with a sticky bar (Promote next step, Pause, Abort) above the bottom bar; tablet 2 columns; laptop timeline plus side panel; large adds live metrics.

### UI-09 Releases tab
- **Purpose:** release history and rollback.
- **Data:** `listReleases`, `getRelease`, `diffReleases` (live release against the target, to build the rollback dialog; `rollbackService` has no `dry_run`).
- **Actions:** `rollbackService` (rollback dialog), `annotateRelease` (note), compare two (UI-35), `promoteRelease` (UI-10).
- **Rollback dialog:** lists what is restored (image, config, variable and secret versions) and what is not (data, volumes), shows the fast-rollback badge ("about 5 seconds") or "needs a rebuild", the plan, a destructive-style button; typed name only on protected.
- **States:** release with pruned image: Roll back disabled, tooltip "Image was removed. Deploy from commit instead", with a link to UI-07.
- **Roles:** read all; rollback `service:rollback` (P); note `service:update` (P).
- **Layout:** cards (phone), table (tablet and up) with number, commit, author, time, status, badge.

### UI-10 Promote dialog
- **Purpose:** promote a release or a whole environment to another environment.
- **Data:** `promoteRelease` or `promoteEnvironment` with `dry_run` (plan: changed fields, environment-specific values kept, missing variables, plan hash, strategy).
- **Actions:** confirm `promoteRelease`, `promoteEnvironment`.
- **States:** missing variables shown in a red list (with icon and word) and block confirm until set; protected target: typed environment name.
- **Roles:** `service:promote`, judged on the **target** environment.
- **Layout:** sheet with a read-only list on phone; dialog with the diff table above.

### UI-11 Variables and secrets
- **Purpose:** manage variables and secrets by scope.
- **Data:** `listVariables`, `listSecrets`.
- **Actions:** `createVariable`, `updateVariable`, `deleteVariable`, `bulkSetVariables` (not on phone), `createSecret`, `setSecretValue`, `deleteSecret`.
- **States:** secret rows show name, version, updated, who ([REQ-UI-005](../11-ui.md#4-rules-and-requirements)); the value field is write-only ("Set new value"); inherited values show the source scope; pending changes banner "Applies on next deploy".
- **Roles:** `variable:read` all; `variable:write`, `secret:write` P; `secret:list` hidden for V.
- **Layout:** cards on phone with edit in a sheet; table above.

### UI-12 Domains
- **Purpose:** domains and TLS.
- **Data:** `listDomains`.
- **Actions:** `createDomain`, `updateDomain`, `deleteDomain`, `moveDomain`.
- **Content:** TLS state, DNS records to create (copy buttons), redirects, wildcard.
- **States:** DNS check pending (warning triangle and text), certificate failed with reason.
- **Roles:** `domain:manage` P; read all.
- **Layout:** cards on phone; table above.

### UI-13 Storage
- **Purpose:** volumes and orphan volumes.
- **Data:** `listVolumes`, `listOrphanVolumes`.
- **Actions:** `createVolume`, `updateVolume`, `deleteVolume` (typed name), `deleteOrphanVolume` (typed name).
- **Roles:** `volume:manage` P; orphan delete `volume:manage`; read all.
- **Layout:** cards (phone), table above, usage bar with number.

### UI-14 Health check settings
- **Purpose:** configure and test health checks.
- **Data:** `getService` (health section).
- **Actions:** `updateService`, `testHealthCheck` ("Test now" shows result and time).
- **States:** defaults shown as placeholder text and as a "default" label.
- **Roles:** `service:update` P.
- **Layout:** one form column on all sizes, result panel beside it on laptop.

### UI-15 Logs
- **Purpose:** live tail and search of one service.
- **Data:** `streamServiceLogs`, `getServiceLogs`, `searchLogs`.
- **Actions:** pause and follow, filters (replica, level, text), wrap, copy line, download visible range, Search mode (text, fields, time range, context).
- **States:** stream gap marker "Gap of 12 s, logs are in search"; masked values show `***`; empty "No logs yet. Logs appear when the service writes output."
- **Roles:** `logs:read` all.
- **Layout:** see responsive table. Viewer rules in [components](components.md#logviewer).

### UI-16 Metrics
- **Purpose:** CPU, memory, network, disk, requests, 5xx, p50/p95/p99.
- **Data:** `getServiceMetrics`.
- **Actions:** range selector (up to retention), step, release markers on, table view toggle.
- **States:** no data yet; range beyond retention shows the limit.
- **Roles:** `metrics:read` all.
- **Layout:** charts 1/2/3/4 per row.

### UI-17 Backups
- **Purpose:** policies, runs, restore for a service or database.
- **Data:** `listBackupPolicies`, `listBackupRuns`, `listRestoreChecks`.
- **Actions:** `createBackupPolicy`, `updateBackupPolicy`, `deleteBackupPolicy`, `runBackup`, `runRestoreCheck`, `restoreBackup` (UI-39).
- **Content:** "Restore verified N days ago" badge ([REQ-UI-033](#requirements)).
- **States:** empty "No backup yet. Add a policy and a target."; no target → link to UI-38.
- **Roles:** read all; policies `backup:manage` P; restore `backup:restore` (A, O only, hidden for D, V).
- **Layout:** cards (phone), tables.

### UI-18 Previews
- **Purpose:** list previews and their settings.
- **Data:** `listPreviews`, `getProject`.
- **Actions:** open preview (UI-36), `createPreview`, `updateProject` (enable, base environment, limits, forks, selector), `deleteEnvironment` (delete preview, typed name).
- **States:** empty "No previews. Open a pull request or create one by hand."; expired and failed previews marked.
- **Roles:** `preview:manage` A, O, D; read all.
- **Layout:** cards (phone) with PR link, URL, state, expiry; table above.

### UI-19 Nodes
- **Purpose:** servers of the cluster.
- **Data:** `listNodes`, `getNodeMetrics`.
- **Actions:** Add node (UI-31 step 2 dialog: `createNodeJoinToken`), open node (UI-40).
- **Content:** state, labels, CPU/RAM/disk bars with numbers, workload count.
- **States:** empty "Only the control-plane server is here. Add a server to run services on more machines."
- **Roles:** `node:read` all; `node:manage` A, O.
- **Layout:** cards, then tables.

### UI-20 Move service
- **Purpose:** move a service to another node.
- **Data:** `listNodes`, `getService`.
- **Actions:** `moveService` (target node, data copy mode, downtime warning).
- **States:** target without capacity or labels disabled with the reason; volumes need data copy and show the size.
- **Roles:** `service:move` P.
- **Layout:** sheet (phone), dialog above.

### UI-21 Catalog
- **Purpose:** template marketplace.
- **Data:** `listTemplates`, `getTemplate`.
- **Actions:** `instantiateTemplate` (form from `inputs`), `createTemplate` (custom, `template:manage` D, A, O).
- **States:** search empty "No template matches. Try another word or import a Compose file."
- **Roles:** `template:read` all; instantiate `service:create` P.
- **Layout:** card grid 1/2/3–4/5–6.

### UI-22 Alerts
- **Purpose:** firing and resolved alerts, rules, mute.
- **Data:** `listAlertInstances`, `listAlertRules`, `listAlertChannels`.
- **Actions:** `muteAlertRule`, open editor (UI-48).
- **States:** none firing "All quiet"; there is no acknowledge operation, so v1 offers Mute only (see [gaps](#known-api-gaps)).
- **Roles:** `alert:read` all; mute `alert:manage` A, O.
- **Layout:** cards (phone), tables.

### UI-23 Settings: Members and Teams
- **Purpose:** people and groups. Two tabs: Members (UI-45), Teams (UI-44).
- **Roles:** `member:read` all; changes `member:manage`, `team:manage` A, O. An admin cannot change an owner (control disabled with reason).

### UI-24 Settings: SSO
- **Purpose:** identity providers.
- **Data:** `listSsoProviders`, `listGroupMappings`.
- **Actions:** `createSsoProvider`, `updateSsoProvider`, `deleteSsoProvider`, `replaceGroupMappings`, "Test login", "SSO required" switch (warns about lock-out and break-glass).
- **Roles:** `sso:manage` A, O; hidden for D, V. Not on phone (read-only).
- **Layout:** list left and form right on laptop.

### UI-25 Settings: Tokens
- **Purpose:** API tokens.
- **Data:** `listTokens`.
- **Actions:** `createToken` (name, scopes, project limit, expiry; the token value is shown once with a copy button and a warning), `revokeToken`, `revokeOrgToken` (others', `token:manage`).
- **Roles:** `token:own` all; `token:manage` A, O.
- **Layout:** cards (phone), table.

### UI-26 Settings: Audit
- **Purpose:** who did what.
- **Data:** `listAuditEvents`.
- **Actions:** filter (actor, action, object, time), `exportAuditEvents`, `verifyAuditChain`; diff viewer (values show `***`).
- **States:** chain verification result as a banner (passed or failed with the first bad entry).
- **Roles:** `audit:read` A, O; export `audit:export`. Hidden for D, V.
- **Layout:** cards and filter sheet on phone (diff not on phone); table above.

### UI-27 Settings: Git connections
- **Purpose:** connect GitHub App, GitLab, Gitea, deploy keys.
- **Data:** `listGitConnections`, `listGitRepos`.
- **Actions:** `createGitConnection`, `deleteGitConnection`, `createDeployKey` (public key shown to copy).
- **Content:** webhook URL with copy.
- **Roles:** `git:manage` A, O; hidden for D, V.

### UI-28 Settings: Organization
- **Purpose:** organization settings.
- **Data:** `getOrg`, `getVersion`.
- **Actions:** `updateOrg` (name, default domain suffix, preview domain, retention, ACME DNS provider, 2FA policy), `deleteOrg` (typed name).
- **Content:** panel version, and an update notice only when the opt-in update check is on ([parity](parity.md) row "Updates of the panel").
- **Roles:** `org:read` all; `org:update` A, O; `org:delete` O.

### UI-29 Config as code
- **Purpose:** link a repo file, see plan and drift.
- **Data:** `exportProjectConfig`, `planProjectConfig`.
- **Actions:** `planProjectConfig` ("Plan now"), `applyProjectConfig`, export download.
- **States:** drift list; sync error with line numbers.
- **Roles:** `config:apply` P; read all. Phone read-only.

### UI-30 Terminal
- **Purpose:** shell in a container (audited).
- **Data:** none.
- **Actions:** `execService` (WebSocket).
- **States:** connecting, connected, closed (with exit code), forbidden on protected for D; a notice "This session is recorded in the audit log" before connect.
- **Roles:** `service:exec` P.
- **Layout:** desktop and tablet only.

## 4. Screens UI-31 to UI-49

### UI-31 Onboarding checklist and first server join
- **Purpose:** get from empty install to a running service ([REQ-UI-025](#requirements)).
- **Steps:** 1 Name the organization (`updateOrg`). 2 Add a server (optional, the control-plane server already runs workloads): name and labels, then the one-line install command with a copy button, the 1 h expiry countdown, and a live row `pending → ready` ([flows](flows.md#4-add-a-server)). 3 Add a domain and check DNS (`createDomain`, `listDomains`). 4 Create the first service (UI-32).
- **Data:** `getOrg`, `listNodes`, `listDomains`, `listServices`, `streamEvents`. Progress is computed from this data, so it is never stale. "Skipped" marks are kept on the device (`localStorage`).
- **Actions:** `createNodeJoinToken` (`node:manage`), then the same as the steps.
- **States:** token expired → "The command expired. Create a new one." with a button; node not connected after 5 min → a checklist of causes (port, firewall, clock); the node row shows `pending`, `ready`, `down`.
- **Roles:** A and O see all steps. D sees steps 3 and 4 only where allowed. V never sees the checklist.
- **Layout:** card list on phone; centered 640 px column above. Resumable from the dashboard card.

### UI-32 Service wizard
- **Purpose:** create a service. Stepper: Source → Build and run → Domain and env → Review.
- **Start paths:** Git, Image, Template, Database (chosen in UI-05, kept in the URL `?source=`).
- **Data:** `listGitConnections`, `listGitRepos`, `listTemplates`, `getTemplate`, `listNodes`, `listDomains`.
- **Actions:** `createService` (git, image, database), `instantiateTemplate`, `importCompose`; optional first `deployService`.
- **Steps per path:** Git: connection and repo, branch, builder (`railpack` default, `dockerfile`), port. Image: reference, registry login, port. Template: inputs from the template (with defaults). Database: engine (`postgres`, `mysql`, `mariadb`, `mongo`, `redis`), version, size, credentials shown once. Then: domain and TLS, variables (key-value editor), placement and resources (advanced, desktop and tablet), Review (plan summary, what will be created).
- **States:** [REQ-UI-031](components.md#requirements): Back keeps data; leaving with data asks first. Validation per step, errors linked to fields. A failed create keeps all data.
- **Roles:** `service:create` P.
- **Layout:** phone one step per screen, sticky Back and Next bar, advanced step hidden ("Open on a larger screen"); laptop stepper left, form, summary right.

### UI-33 Environments list and create
- **Purpose:** list environments of a project and create one.
- **Data:** `listEnvironments`, `getEnvironment`.
- **Actions:** `createEnvironment` (name, kind, copy from), `updateEnvironment` (rename, protected flag: A and O only), `deleteEnvironment` (typed name, option "delete volumes" off by default).
- **Roles:** `env:create` D, A, O; protected flag needs A; delete P.
- **Layout:** cards (phone), table.

### UI-34 Deployments list
- **Purpose:** recent deployments across services of a scope.
- **Data:** `listServiceDeployments` for the services of the chosen project or environment, merged by time, plus `streamEvents` (the API has no org-wide list, see [gaps](#known-api-gaps)).
- **Actions:** open UI-08, `cancelDeployment`, filters (state, service, environment).
- **States:** empty "No deployment yet."; loads the 30 most recent per service.
- **Roles:** `deployment:read` all.
- **Layout:** cards (phone), table.

### UI-35 Release diff
- **Purpose:** compare two releases (image, config, variable and secret versions, resources).
- **Data:** `diffReleases`.
- **Actions:** copy as text, swap sides.
- **Content:** secret values show `***` with version numbers only.
- **Roles:** `deployment:read` all.
- **Layout:** desktop side by side, tablet unified, phone summary list plus notice.

### UI-36 Preview detail
- **Purpose:** one preview environment.
- **Data:** `getEnvironment` (its `preview` block: repo, PR, state, URL, expiry), `listServices`.
- **Actions:** open URL, redeploy (`deployService`), extend expiry (`updateEnvironment`), delete (`deleteEnvironment`, typed name).
- **States:** `failed` shows the reason (for example a missing secret that is not marked `preview`).
- **Roles:** `preview:manage`; read all.

### UI-37 Logs search
- **Purpose:** search logs across services.
- **Data:** `searchLogs`, `listServices`.
- **Actions:** query, fields, time range (up to the 30 day retention), service filter, open context around a line, save the link.
- **States:** no match "Nothing found in this range. Widen the time range."; retention limit note.
- **Roles:** `logs:read` all.
- **Layout:** as UI-15.

### UI-38 Backup targets
- **Purpose:** where backups go (S3, SFTP and others of [F10](../04-features/F10-backups-and-restore-checks.md)).
- **Data:** `listBackupTargets`.
- **Actions:** `createBackupTarget`, `updateBackupTarget`, `deleteBackupTarget`, `testBackupTarget` (shows result).
- **States:** keys are write-only; test failure shows the reason.
- **Roles:** `backup:manage` P (A, O on protected); read all.

### UI-39 Restore wizard and restore-check result
- **Purpose:** restore from a run, and see restore checks.
- **Steps:** pick a run → pick target (in place or a new service) → check summary (what is replaced, size, time estimate, data loss warning) → typed confirm for in place → progress → result ([flows](flows.md#5-restore)).
- **Data:** `listBackupRuns`, `listRestoreChecks`, `listServices`.
- **Actions:** `restoreBackup`, `runRestoreCheck`.
- **States:** check result with per-step list (restored, queried, row counts), passed or failed with reason.
- **Roles:** restore `backup:restore` (A, O); check run `backup:manage` P. Hidden for V.
- **Layout:** one step per screen on phone.

### UI-40 Node detail
- **Purpose:** one node.
- **Data:** `getNode`, `getNodeMetrics`, `listOrphanVolumes`, `listServices` (workloads).
- **Actions:** `updateNode` (labels, name), `cordonNode`, `uncordonNode`, `drainNode`, `setNodeMaintenance`, `runNodeSelfCheck`, `deleteNode` (typed name), `deleteOrphanVolume`.
- **Content:** CPU/RAM/disk bars with numbers, agent version, last heartbeat, self-check results, disk space and orphan volumes (the cleanup place, see [parity](parity.md)).
- **States:** `down` or `suspect` show the time since last heartbeat; drain shows progress per service.
- **Roles:** `node:read` all; `node:manage` A, O. Actions blocked by state (already cordoned) are disabled with the reason.

### UI-41 Failover and move history
- **Purpose:** what moved and why.
- **Data:** `listAuditEvents` filtered to node and service moves, plus `streamEvents`.
- **Content:** timeline: time, service, from node, to node, cause (manual move, drain, failover), result.
- **Roles:** needs `audit:read` for the audit-based list (A, O). Others see the events of services they can read in the service's Deployments tab only.

### UI-42 Command palette and shortcut help
- **Purpose:** keyboard entry. See [navigation](navigation.md#4-command-palette-and-global-search).
- **Data:** cached lists (`listProjects`, `listServices`, `listNodes`, `listTemplates`).
- **Roles:** shows only allowed commands.

### UI-43 Profile, sessions, 2FA, theme
- **Purpose:** the user's own settings.
- **Data:** `getMe`, `listMySessions`.
- **Actions:** `updateMe` (name, password), `revokeMySession`, `enrollTotp`, `verifyTotp`, `disableTotp`, theme (System, Light, Dark), table density, single-key shortcuts on or off, language (English only in v1).
- **States:** TOTP enrollment shows a QR code and a text key, and recovery codes once.
- **Roles:** all (`authenticated`).

### UI-44 Teams
- **Purpose:** groups of members.
- **Data:** `listTeams`, `listMembers`, `listProjectRoles`.
- **Actions:** `createTeam`, `updateTeam`, `deleteTeam`, `addTeamMember`, `removeTeamMember`, `replaceProjectRoles` (project role overrides).
- **Roles:** `team:manage` A, O; read for others (V sees the list only).

### UI-45 Org members
- **Purpose:** people and roles.
- **Data:** `listMembers`.
- **Actions:** `inviteMember` (email, role), `updateMember` (role), `removeMember`, `revokeMemberSessions`.
- **States:** last owner cannot be removed or demoted (disabled with reason). Invitation pending state with resend.
- **Roles:** `member:manage` A, O. An admin cannot change an owner.

### UI-46 Notifications bell
- **Purpose:** quick list of firing alerts and recent failures.
- **Data:** `listAlertInstances`, `streamEvents`.
- **Actions:** open the item; "Mute" for alerts (`muteAlertRule`, A and O).
- **States:** empty "Nothing needs you right now."
- **Roles:** `alert:read` all.
- **Layout:** popover (laptop), sheet (phone).

### UI-47 Error pages
- **Purpose:** 404, 403, offline, server unreachable, session expired.
- **Content:** what happened, why, one next step (go to the dashboard, retry, sign in again). The offline page keeps the last data visible where cached, marked stale.
- **Actions:** `getHealth` (retry check).
- **Roles:** public layout.

### UI-48 Alert channels and rules editor
- **Purpose:** where alerts go and when they fire.
- **Data:** `listAlertChannels`, `listAlertRules`.
- **Actions:** `createAlertChannel`, `updateAlertChannel`, `deleteAlertChannel`, `testAlertChannel` (shows delivery result), `createAlertRule`, `updateAlertRule`, `deleteAlertRule`, `muteAlertRule`.
- **States:** channel secrets (bot token, webhook URL) are write-only; test failure shows the reason; rule preview shows current value against threshold.
- **Roles:** `alert:manage` A, O. Others read in UI-22. Editor is read-only on phone.

### UI-49 Secret versions panel
- **Purpose:** version history of one secret (no values).
- **Data:** `listSecretVersions`.
- **Actions:** `restoreSecretVersion` (makes a new version from an old one), `destroySecretVersion` (typed name, needs `secret:destroy`), `setSecretValue` (new version).
- **Content:** version, time, who, which releases use it ("used by v41, v42").
- **States:** a version used by a retained release cannot be destroyed (disabled with the release numbers).
- **Roles:** `secret:list` D, A, O; write P; destroy A, O.
- **Layout:** side panel (laptop), sheet (phone, read-only list and restore).

## 5. Known API gaps

<a id="known-api-gaps"></a>

The UI is built on the current [openapi.yaml](../openapi.yaml). These items need a server change or a UI workaround. The workaround is the v1 rule. They are reported to the Team Lead.

| Gap | v1 UI rule | Server change that would remove the workaround |
|---|---|---|
| `updateMe` accepts only name and password, so there is no server-side theme or preference field | Theme, density and shortcut choices are saved on the device in `localStorage` ([REQ-UI-015](design-system.md#requirements)) | Add `preferences` to `updateMe` and `Me` |
| No org-wide deployments list | UI-34 merges `listServiceDeployments` of the services in scope | an org-wide deployments list with filters |
| No alert acknowledge operation | UI-22 and UI-46 offer Mute only | an acknowledge operation |
| No delete operation for a preview other than `deleteEnvironment` | UI-18 and UI-36 use `deleteEnvironment` | none needed |
| `getMe` returns the role, not the permission list | The UI uses a permission table generated from the [matrix](../10-security.md#permission-matrix) at build time | Add `permissions` to `Me` |
| `rollbackService` has no `dry_run` | The rollback dialog is built from `getRelease` and `diffReleases` | Add `dry_run` to `rollbackService` |
| No manual Docker prune or cleanup operation | Disk use and orphan volumes are shown in UI-40, cleanup is automatic image GC ([REQ-REL-006](../04-features/F02-rollback-and-releases.md)) | none needed |
| No onboarding progress storage | Progress is derived from data, "skipped" is kept on the device | none needed |
