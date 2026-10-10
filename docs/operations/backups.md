# Backups and restore checks

Polipan backs up databases and volumes with [restic](https://restic.net). Every policy has its own restic
repository on each of its targets. All data is encrypted by restic on the node before it leaves it.

## What the node needs

The agent runs restic. The agent image has restic and an ssh client. On a node where the agent runs as a
systemd service, install both programs (for example `apt install restic openssh-client`). restic 0.17 or newer.

The agent must see the Docker volume folders (`/var/lib/docker/volumes`). A service of the agent on the host sees them.
The agent in a container needs `-v /var/lib/docker/volumes:/var/lib/docker/volumes`. A `local` target is a folder on the node
inside `/var/lib/polipan/backups`. Mount it too when the agent runs in a container.

## Targets

Add targets in **Backups → Backup targets** (S3 compatible, Backblaze B2, SFTP, local folder, Azure Blob, Google Cloud Storage).
Keys are write-only. Press **Test** to write and read a small test repository.

SFTP uses a private key. The first **Test** of an SFTP target saves the host key that the node saw in the target
(`known_host`). From then on every job checks it strictly (`StrictHostKeyChecking=yes`). You can also put the `known_hosts`
line in the target yourself. If the server changes its key, the jobs fail until you give the new line.

S3 endpoints and SFTP hosts must be public addresses. A loopback, link-local, mesh, Docker or private address is refused, unless
the organization setting `allow_private_targets` is on. The result of **Test** is a fixed message. The details are in the server log.
To change a target that a policy of a protected environment (or the control-plane policy) uses, you need the admin role.

## Policies, runs and restore checks

- **Hooks must not hold secrets.** The command of a pre or post hook is stored in the policy and shown in the API, the audit log
  and the run history. Never put a password or a key in it. Read it from the environment of the container instead.

- A policy has a schedule (cron, UTC), one or more targets, a retention (`keep_daily`, `keep_weekly`, `keep_monthly`) and a restore check.
  A retention object with only some keys uses only those rules. Without a retention the default is 7 daily, 4 weekly, 6 monthly.
- The same snapshot goes to every target. A failing target does not stop the others. The run is `failed` when any target failed.
- After the first good run, and then every week, Polipan restores the latest snapshot into a temporary container
  (without network and with limits) and tests it. Targets take turns. A failed check raises the critical alert
  `restore_check_failed`. No passed check in twice the check interval raises `restore_check_overdue`.
- Alerts are written as `alert` events. The delivery to Telegram, Slack and others comes with M8.
- A restore needs `backup:restore` (admin). Restore into a **new service** makes a copy and leaves the original alone. Restore **in place**
  asks for the service name, makes a safety backup first, and has downtime. Volumes and Redis are stopped while the files are replaced.
  Only databases can be restored into a new service.

## Restore with the restic binary alone

The repositories are plain restic repositories (REQ-OSS-006). Print the password and the repositories of a policy:

```sh
docker compose -f /opt/polipan/docker-compose.yml exec server /polipan-server admin backup-password --policy pol_XXXX
```

The command writes an audit record. Then use restic with the credentials of the target:

```sh
export RESTIC_PASSWORD='<password from the command>'
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
restic -r s3:https://s3.example.com/bucket/path/pol_XXXX snapshots
restic -r s3:https://s3.example.com/bucket/path/pol_XXXX dump latest /dump.pgdump | pg_restore -d mydb --no-owner
```

Dump names inside a snapshot: PostgreSQL and the control plane `dump.pgdump` (custom format), MySQL and MariaDB `dump.sql`,
MongoDB `dump.archive`, Redis `dump.rdb`. A volume policy stores the files of the volume.

## The control-plane database

Polipan makes a daily policy for its own database (`0 3 * * *`) with a local target on the control-plane host
(`/var/lib/polipan/backups/control-plane`). The dump holds the encrypted secrets only. **The master key is not in it.**
Keep `master.key` safe by hand. A local target is on the same server: add a remote target to the policy, the page shows a warning until you do.

To restore it by hand: stop the server, restore the dump with `pg_restore --clean --if-exists` into the database, put the same
`master.key` back, start the server.
