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

SFTP uses a private key. Without a host key in the target, the key of the server is trusted the first time.
Put the `known_hosts` line in the target to pin it.

## Policies, runs and restore checks

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
