# Apps and deploys

## Services

A service is one app, database or worker. It lives in an environment of a project. Its settings are:

| Setting | Meaning |
|---|---|
| Source | A Docker image, or a Git repository with a branch and a builder (`dockerfile` or `railpack`). |
| Port | The port the app listens on inside the container. |
| Health check | `tcp` (default), `http` with a path, or a `command`. |
| Resources | CPU and memory limits. |
| Replicas | Number of copies, spread over nodes. |
| Strategy | How a new release replaces the old one. |

## Releases and deployments

A **release** is a frozen set: the image digest, the config and the variable and secret versions.
A **deployment** is one try to make a release live. Every release keeps its image digest, so the same release
always runs the same bytes.

Deployment states: `queued`, `building`, `pulling`, `health_checking`, `switching`, `live`, or `failed`,
`rolled_back`, `cancelled`, `superseded`.

## Strategies

| Strategy | What happens | Downtime |
|---|---|---|
| `blue_green` (default) | The new copy starts next to the old one. When it is healthy, traffic switches at once. The old copy stays stopped for a fast rollback. | None |
| `canary` | Traffic moves in steps (for example 10, 25, 50, 100 %). A gate on errors and latency can stop the rollout and send traffic back. | None |
| `recreate` | Stops the old copy, then starts the new one. | Short |

A service with a `single_writer` volume (for example a database) always uses `recreate`. The deployment shows the
warning `zero_downtime_not_available`.

If the new copy does not become healthy, the old one keeps serving and the deployment fails.
Set `drain_seconds` to give open connections (for example WebSockets) time to end before the old copy stops.

## Roll back

Press **Roll back** on a release, or run `polipan rollback <service> [release]`.
If the stopped containers of that release still exist, traffic switches back at once and nothing is pulled.
Otherwise the image is pulled by digest. A rollback never builds. Domains, volumes and the replica count do not change,
and the variables are those of the old release. Rolling back to the live release answers `409 already_live`.
On a protected environment only an admin can roll back.

## Health checks

A copy is healthy after three good checks. A service without a configured check gets a TCP check on its port.
A `start_period` lets slow apps boot without counting failures. If a running copy becomes unhealthy, it is restarted.

## Stop, start, restart, exec

- **Stop** and **Start** keep the config and the volumes. A stopped service answers `503` with a Polipan page.
- **Exec** opens a shell in a running container (**Terminal** tab). It needs the `service:exec` permission, is audited,
  and ends after 15 minutes without input.

## Volumes

Volumes keep data between deploys. A deleted service keeps its volumes as orphans until you remove them.
Back them up: see [Backups](../operations/backups.md).
