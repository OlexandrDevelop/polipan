# Upgrade guide

## Update from the panel (the easy way)

A panel that was installed with the install script updates itself. Open **Settings, Organization, Updates**:

- **Check for updates** asks the server to look for a new version. The panel shows the version that is ready.
- **Update now** updates the panel. The panel is not available for about a minute. Your apps keep running.
- **Update automatically every night** is on after a new install. The server looks for a new version every night
  (at about 03:30 server time) and updates when there is one.

The same from a terminal: `polipan update status`, `polipan update check`, `polipan update apply`,
`polipan update auto-on` and `polipan update auto-off`.

How it works: the install script sets up a small program on the host, `polipan-update`, and a systemd timer and path
unit (`polipan-update.timer`, `polipan-update.path`). The panel does not update itself. It writes a request in
`/var/lib/polipan/updater`, and the host does the work as root:

1. It makes a copy of the database, `/var/lib/polipan/backups/pre-update-<time>.sql.gz` (the last three are kept).
2. It runs the install script of the newest version again, with the options of the first install.
3. When the new server does not become healthy, it puts the old images back. The state is then `rolled_back`, and
   `/var/lib/polipan/updater/update.log` says why.

A pre-release or a development install (the script from `main`) follows the images of `main`. A release install
follows the newest release. By hand on the host: `sudo polipan-update check` and `sudo polipan-update apply`.
A database migration only moves forward: a rollback of the images does not undo it. Restore the copy of the database
if you must go back after a migration.

## Update by hand

## Before you upgrade

1. Read the release notes of the new version.
2. Make a backup of the control-plane database (the control-plane backup policy, or `pg_dump`).
3. Make sure `/etc/polipan/master.key` is saved outside the server.
4. Check **Nodes**: all nodes should be `ready`.

## Upgrade the control plane

On the control-plane host run the install script of the new release again:

```sh
curl -fsSL https://github.com/OlexandrDevelop/polipan/releases/download/v1.1.0/install.sh | sudo sh -s -- --domain panel.example.com
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
