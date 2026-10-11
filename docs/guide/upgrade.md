# Upgrade guide

## Before you upgrade

1. Read the release notes of the new version.
2. Make a backup of the control-plane database (the control-plane backup policy, or `pg_dump`).
3. Make sure `/etc/polipan/master.key` is saved outside the server.
4. Check **Nodes**: all nodes should be `ready`.

## Upgrade the control plane

On the control-plane host run the install script of the new release again:

```sh
curl -fsSL https://github.com/polipan/polipan/releases/download/v1.1.0/install.sh | sudo sh -s -- --domain panel.example.com
```

Use the same `--domain` and `--dir` as the first time. The script keeps the master key, the `.env` file and the data,
checks the checksums, pulls the new images by digest and restarts the server.
The script also replaces the agent binary of this host and restarts the agent service, so the host runs the new version.
Database migrations run when the server starts. A migration only moves forward: to go back, restore the backup.

Your apps keep running during the restart. Domains keep working, because every node has its own ingress and its own
state. The agents reconnect and the state converges.

## Upgrade the nodes

The agent of each node is updated by running `install-agent.sh` of the new release on that node. A node of the previous minor version keeps working, so you can update nodes one at a time.
The CLI warns when it is more than one minor version older than the server.

## Check

- `/healthz` answers `ok` and `polipan version` shows the new version.
- `polipan audit verify` reports a valid chain.
- Open a service and check that it is `live`.

## Roll back an upgrade

1. Stop the server: `docker compose -f /opt/polipan/docker-compose.yml stop server`.
2. Restore the database backup.
3. Install the old version with `--version`.

## Tested upgrade path

`make upgrade-test FROM=<tag>` starts the old server on a new database, makes data (user, project, token, session),
starts the new server on it, and checks the migrations, the login, the token, the project and the audit chain.

## Upgrade notes by version

### 1.0

- The Go toolchain is 1.26. If you build from source, you need Go 1.26 or newer.
- New operations: `deleteOrg` and `moveDomain`. They need no change on your side.
- The SSO redirect address is built only from `external_url`. Set `external_url` (or `POLIPAN_EXTERNAL_URL`) before
  you use SSO. A request `Host` header is no longer used.
