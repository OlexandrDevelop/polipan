# Key Flows

The deploy engine states are in [08-deploy-engine](../08-deploy-engine.md#2-state-machine). The flows here show what the UI does at each step.

## 1. Deploy

```mermaid
sequenceDiagram
  participant U as User
  participant UI as UI
  participant API as API
  U->>UI: Click Deploy (or press d)
  UI->>API: deployService dry_run
  API-->>UI: plan (strategy, forced?, downtime, warnings)
  UI-->>U: Deploy dialog with the plan
  U->>UI: Confirm
  UI->>API: deployService (Idempotency-Key)
  API-->>UI: 202 deployment
  UI->>API: streamDeploymentEvents
  API-->>UI: queued, building, starting, health_checking, switching, draining, live
  UI-->>U: Deployment page, success banner with Roll back for 10 min
```

Canary variant:

```mermaid
flowchart TD
  A[Deploy with strategy canary] --> B[Step 1: small share of traffic]
  B --> G{Gate values ok?}
  G -- yes, auto --> N[Next step]
  G -- yes, manual --> W[Wait: user presses Promote next step]
  W --> N
  N --> L{Last step?}
  L -- no --> G
  L -- yes --> LV[live]
  G -- no --> R[Auto rollback]
  R --> T[Banner and bell item: Canary stopped, traffic is back on the old release, reason]
  B --> P[User: Pause or Abort at any time]
  P --> R2[Abort: roll back]
```

If the gate fails, the UI shows the banner on the deployment page (not only a toast) with the failed gate value and the threshold ([REQ-UI-032](components.md#requirements)).

## 2. Rollback

1. Releases tab (or the "Roll back" shortcut after a deploy) → pick a release → Roll back.
2. Dialog: what is restored (image, config, variable and secret versions) and what is not (data, volumes). Fast rollback badge ("about 5 seconds") or "needs a rebuild". The dialog is built from `getRelease` and `diffReleases` (live release against the target), because `rollbackService` has no `dry_run`.
3. Protected environment: banner "Production" and typed environment name.
4. Confirm → `rollbackService` → deployment page with the same timeline.
5. Result: the new live release shows "rollback of v41". The bell does not fire for the user's own action.

```mermaid
flowchart LR
  R[Releases tab] --> D[Rollback dialog with plan]
  D -->|confirm| DP[Deployment page]
  DP --> OK[Live: rollback of vN]
  DP --> F[Failed: banner with reason and Retry]
```

## 3. Promote staging to production

1. Staging service → Promote (or environment page → Promote all).
2. Pick target `production`. UI calls the promote operation with `dry_run`.
3. Diff table: changed fields, environment-specific values kept, missing variables in a red list (icon and word). Missing variables block Confirm until they are set in the target.
4. Strategy and downtime shown as in the deploy dialog.
5. Protected target: typed environment name, and admin needed (a developer sees the button disabled with the reason).
6. Confirm → deployment page of the production service. For a batch, a list of deployments in dependency order.

```mermaid
flowchart TD
  S[Promote] --> T[Pick target]
  T --> PL[Plan with dry_run]
  PL --> M{Missing variables?}
  M -- yes --> FIX[Set variables in target] --> PL
  M -- no --> C[Confirm, typed name if protected]
  C --> DEP[Deployment page of target]
```

## 4. Add a server


```mermaid
sequenceDiagram
  participant U as User
  participant UI as UI
  participant API as API
  participant N as New server
  U->>UI: Nodes, Add node (name, labels)
  UI->>API: createNodeJoinToken
  API-->>UI: one-line install command, expires in 1 h
  UI-->>U: Command with Copy button, countdown, row pending
  U->>N: Run the command
  N->>API: agent joins (mTLS)
  API-->>UI: node event via streamEvents
  UI-->>U: Row turns ready (live)
```

If the countdown ends, the UI offers "Create a new command". After 5 min in `pending` the UI shows the checklist of causes (port, firewall, clock).

## 5. Restore


```mermaid
flowchart TD
  A[Backups tab: pick a run] --> B[Pick target: in place or new service]
  B --> C[Check summary: what is replaced, size, estimate]
  C --> D{In place?}
  D -- yes --> E[Typed confirm of the service name]
  D -- no --> F[Confirm]
  E --> G[Progress: live steps]
  F --> G
  G --> H[Restore result: passed or failed with steps]
  H --> I[Offer: Run a restore check. Only a check updates the badge]
```

A failed restore shows the reason and keeps the original data untouched where the engine allows it (the text says which case applies).

## 6. First-run onboarding

```mermaid
flowchart LR
  S[Setup link] --> O[Create owner]
  O --> ORG[Name organization]
  ORG --> N[Add server, optional]
  N --> DN[Add domain and DNS check, optional]
  DN --> SV[Create first service]
  SV --> DP[Deployment page]
  DP --> URL[Open the live URL]
```

Every step can be skipped. The dashboard shows the open steps until all are done or dismissed.
