# Low-Fidelity Wireframes

Text wireframes. They show structure and order, not final looks. The clickable [prototype](../../design/ui-prototype.html) shows color, theme and phone behavior. `[ ]` is a button, `( )` a select, `●▲✕■⚡` are the status icons (always with a word).

## 1. Dashboard (laptop, 1280 px)

```text
+--------------+--------------------------------------------------------------+
| Polipan      |  Search or press Ctrl+K              [theme] [bell 2] [avatar] |
| (Acme ▾)     +--------------------------------------------------------------+
| Dashboard *  |  Dashboard                                                   |
| Projects     |  +-------------+ +-------------+ +-------------+ +----------+ |
| Deployments  |  | Services    | | Alerts      | | Nodes       | | Needs    | |
| Previews     |  | ● 18 run    | | ✕ 1 critical| | ● 3 ready   | | attention| |
| Nodes        |  | ▲ 2 deploy  | | ▲ 2 warning | | ▲ 1 suspect | |  3       | |
| Catalog      |  | ■ 1 stopped | |             | |             | |          | |
| Alerts       |  +-------------+ +-------------+ +-------------+ +----------+ |
| Logs         |                                                              |
| Backups      |  Recent deployments                    Node health            |
| Settings     |  api     prod  ● live      2 min       nod-eu1  ● ready  CPU 41% |
|              |  web     stag  ▲ deploying 40 s        nod-eu2  ● ready  CPU 22% |
| Production ▾ |  worker  prod  ✕ failed    1 h         nod-us1  ▲ suspect        |
|              |  [View all]                           [View nodes]           |
+--------------+--------------------------------------------------------------+
```

## 2. Service detail (laptop)

```text
+--------------+--------------------------------------------------------------+
| (sidebar)    | Shop › Production › api                                      |
|              | api   ● Running   [Production] [Protected 🔒]                 |
|              | [Deploy to production]  [Roll back]  [⋯ Restart Stop Move Delete] |
|              | +----------------------------------------------------------+ |
|              | | Production. Deploys need admin for developers.           | |
|              | +----------------------------------------------------------+ |
|              | Overview | Deployments | Releases | Domains | Env & secrets |  |
|              | Volumes | Health | Logs | Metrics | Terminal | Backups | Settings |
|              | +--------------------+ +--------------------+ +-------------+ |
|              | | Live release v42   | | Replicas           | | URLs        | |
|              | | abc123 by Sam 2 h  | | #1 nod-eu1 ● ok    | | api.acme.io | |
|              | | image digest       | | #2 nod-eu2 ● ok    | | TLS ● valid | |
|              | +--------------------+ +--------------------+ +-------------+ |
|              | +----------------------------------------------------------+ |
|              | | Deployment in progress v43  ▲ health_checking  [Open]    | |
|              | +----------------------------------------------------------+ |
|              | Last 24 h:  CPU ▁▂▃▂▂▃   Memory ▃▃▃▄▄▄   5xx ▁▁▁▁▁▂          |
+--------------+--------------------------------------------------------------+
```

## 3. Deploy view, canary (laptop)

```text
+--------------------------------------------------------------------------------+
| api › Deployment v43 (canary)    ▲ shifting     [Pause] [Abort and roll back]   |
| [Production]                                                                    |
+------------------------------------------+-------------------------------------+
| Timeline                                 | Traffic                             |
| ● queued                       0:00      | old v42  |██████████████▒▒▒▒▒| 75 %    |
| ● building                     1:12      | new v43  |▒▒▒▒▒██████████████| 25 %    |
| ● starting                     0:08      | Canary 25 %, step 2 of 4            |
| ● health_checking              0:15      | Gate: 5xx 0.2 % (max 1 %)  ● pass   |
| ▲ shifting  step 2 of 4        now       | Gate: p95 180 ms (max 300) ● pass   |
| ■ draining                     -         | [Promote to next step]              |
| ■ live                         -         | Replicas                            |
|                                          | v43 #1 nod-eu1 ● healthy            |
| Build log (collapsed)  [Expand]          | v43 #2 nod-eu2 ● healthy            |
+------------------------------------------+-------------------------------------+
```

Blue-green view: same page, the traffic box shows two rows (blue = old, green = new) at 0 / 100 % and one switch step; there is no gate box.

## 4. Phone layout (360 px)

```text
+--------------------------+      +--------------------------+
| Shop · Production ▾  🔍 🔔 |      | ‹ api  ● Running        |
+--------------------------+      | [Production]             |
| Dashboard                |      | [Deploy to production]   |
| +----------+ +---------+ |      | Overview Deployments  ▸  |
| | ● 18 run | | ✕ 1 alert| |      | +----------------------+ |
| +----------+ +---------+ |      | | Live v42  abc123     | |
| +----------+ +---------+ |      | | Sam · 2 h ago        | |
| | ● 3 nodes| | ▲ 3 need| |      | +----------------------+ |
| +----------+ +---------+ |      | | Replicas  2 ● ok     | |
| Recent deployments       |      | +----------------------+ |
| +----------------------+ |      | | api.acme.io  TLS ●   | |
| | api  ● live    2 min | |      | +----------------------+ |
| | prod            [⋯]  | |      |                          |
| +----------------------+ |      | [Roll back]  [⋯]         |
| | web  ▲ deploying 40 s| |      +--------------------------+
| +----------------------+ |      | Home Proj Deploys Nodes More |
+--------------------------+      +--------------------------+
| Home Proj Deploys Nodes More |
+--------------------------+
```

Deployment page on phone: one column; timeline, then traffic box; a fixed bar above the bottom navigation with `[Promote next step] [Pause] [Abort]`.

## 5. Command palette

```text
+------------------------------------------------------+
| 🔍  deploy a|                                  Esc    |
+------------------------------------------------------+
| Commands                                             |
|  Deploy api…                         service:deploy  |
|  Deploy admin-panel…                                 |
| Services                                             |
|  api          Shop › Production      ● Running       |
| Pages                                                |
|  Alerts                                              |
| Search logs for "deploy a"                           |
+------------------------------------------------------+
|  ↑↓ move   Enter open   > commands   / pages   ? help |
+------------------------------------------------------+
```

## 6. Rollback dialog

```text
+--------------------------------------------------+
| Roll back api to v41                    [Production] |
| This starts v41 again. v42 stays available.         |
| ⚡ Fast rollback, about 5 seconds                    |
| Restored:      image, config, variables v7, secrets v3 |
| Not restored:  data in volumes, database content     |
| Type "production" to confirm: [______________]       |
|                          [Cancel]  [Roll back to v41] |
+--------------------------------------------------+
```
