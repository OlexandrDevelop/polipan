# C06 – Volumes

## Summary

A service can mount persistent storage. Volumes live on one node (local Docker volumes). Volumes decide whether a service can use zero-downtime deploys.

## User stories

- **US-VOL-01** As a developer, I add a volume to keep uploads after redeploys.
- **US-VOL-02** As a developer, I see volume size and usage.
- **US-VOL-03** As an admin, I back up a volume ([F10](F10-backups-and-restore-checks.md)).
- **US-VOL-04** As a developer, I mount a config file from text I paste in the UI.

## Rules

- **REQ-VOL-001** (MUST) Volume kinds: `volume` (named Docker volume, default), `bind` (a path under `/var/lib/polipan/binds/<service>` only), `tmpfs`. Fields: `name`, `mount_path`, `access_mode`, optional `size_limit_bytes`.
- **REQ-VOL-002** (MUST) `access_mode`: `single_writer` (default) or `shared`. A `single_writer` volume means at most one running container at a time (see [08](../08-deploy-engine.md#3-strategy-selection)). `shared` is allowed only when the user declares the app safe for concurrent writers or the mount is read-only for one side.
- **REQ-VOL-003** (MUST) A local volume is pinned to the node where it was created (`volumes.node_id`). The scheduler places the service on that node ([09](../09-multi-server.md#7-placement-and-scheduling)).
- **REQ-VOL-004** (MUST) Deleting a service does not delete its volumes unless the user confirms. Volumes of deleted services are listed as "orphan" with size and can be deleted.
- **REQ-VOL-005** (MUST) Volume usage (bytes, inodes) is reported by the agent every 60 s and available as metrics.
- **REQ-VOL-006** (SHOULD) File mounts: the user pastes file content (up to 1 MiB) with a path. It is stored in the service config (not as a secret) and written to the container read-only. A secret file uses `secret_files` ([C05](C05-env-vars.md)).
- **REQ-VOL-007** (MUST) Moving a volume to another node uses the move flow of [09](../09-multi-server.md#7-placement-and-scheduling) and always has downtime.
- **REQ-VOL-008** (MUST) Preview environments get their own empty volumes. They never mount a volume of another environment ([F03](F03-preview-environments.md)).
- **REQ-VOL-009** (SHOULD) `size_limit_bytes` is enforced by a quota where the filesystem supports it (XFS project quota); otherwise it is only an alert threshold.

## Edge cases

- Volume node is `down`: service cannot start elsewhere unless failover with data loss opt-in applies.
- Mount path conflicts (two volumes on the same path): rejected 422.
- Volume name reuse after delete: allowed. A new empty volume is created.
- A blue-green request with a `single_writer` volume: forced `recreate` with a warning ([F01](F01-zero-downtime-deploy.md)).

## Touch points

API: `/services/{id}/volumes`, `/volumes/{id}`, `/nodes/{id}/orphan-volumes`. CLI: `polipan volume add|ls|rm`. Config: `services.<name>.volumes`. UI: Service → Storage.

## Acceptance criteria

- **AC-VOL-01** Given a volume with data, When the service is redeployed, Then the data is still there. (REQ-VOL-001)
- **AC-VOL-02** Given a service with a `single_writer` volume, When deployed with `blue_green`, Then the deployment runs as `recreate` with warning `zero_downtime_not_available`. (REQ-VOL-002)
- **AC-VOL-03** Given a service deleted without volume confirmation, When listed, Then its volumes appear as orphans and the data exists. (REQ-VOL-004)
- **AC-VOL-04** Given a preview environment, When created, Then none of its volumes is the volume of its base environment. (REQ-VOL-008)

## Out of scope

Network volumes (NFS, Ceph) as first-class types in v1 (a `bind` of a host-mounted NFS path under the binds directory works).
