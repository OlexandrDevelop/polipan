# F10 – Backups with automatic restore checks and many storage targets — Gap 10

## Summary

Users back up databases and volumes on a schedule to one or more storage targets. Polipan tests the backups itself by restoring them into a temporary container on a schedule. A backup that was never restored is not trusted. Engine: restic ([02](../02-architecture.md#2-tech-stack-and-reasons)). This file also delivers the basic backup core feature [C08](C08-backups-basic.md).

Numbering note: `REQ-BKP-001` to `REQ-BKP-011` are the backup rules. `REQ-BKP-100` to `REQ-BKP-106` are the restore check rules. Both are in this file.

## User stories

- **US-BKP-10** As a developer, I set a daily backup of my Postgres to S3 and Backblaze at the same time.
- **US-BKP-11** As an admin, I get an alert when a restore check fails, before I need the backup.
- **US-BKP-12** As an admin, I restore a backup into the service, or into a new service to look at it.
- **US-BKP-13** As an admin, I know the control-plane database is backed up too.

## Rules

- **REQ-BKP-001** (MUST) A backup policy belongs to one service (database or volume). Fields: `schedule` (cron, UTC), `target_ids` (one or more), `retention` (`keep_daily`, `keep_weekly`, `keep_monthly`; default from [glossary](../01-glossary.md#defaults)), `restore_check_schedule` and `restore_check_enabled`.
- **REQ-BKP-002** (MUST) Backup storage targets: `s3` (any S3-compatible), `b2`, `sftp`, `local` (path on a node), `azure`, `gcs`. A target has a "test" action. The same snapshot is written to every target of the policy. A target failing does not fail the others; the run is `failed` if any target failed, with per-target results.
- **REQ-BKP-003** (MUST) Backup method by type: `postgres` `pg_dump` (custom format), `mysql`/`mariadb` `mysqldump --single-transaction`, `mongo` `mongodump --archive`, `redis` RDB snapshot, volumes: restic file backup of the volume path (container stopped is not required; the UI notes crash-consistency). Dumps are piped into restic (no temp file on the node).
- **REQ-BKP-004** (MUST) restic repository passwords are generated per policy, stored as secrets, and never shown. Target credentials are secrets. All data is encrypted client-side by restic.
- **REQ-BKP-005** (MUST) Retention is applied with `restic forget --prune` after each successful run on each target.
- **REQ-BKP-006** (MUST) Manual backup now: `POST /backup-policies/{id}/run`.
- **REQ-BKP-007** (MUST) Restore: from any snapshot, to (a) the same service (needs `backup:restore`, a confirm with the service name, and a safety backup first), or (b) a **new** service (clone) to inspect. Database restore stops the service, restores, starts. This has downtime and says so.
- **REQ-BKP-008** (MUST) The backup list shows for each run: time, size, per-target result, snapshot id, duration, and the last restore check status for the policy.
- **REQ-BKP-009** (MUST) Backups run on the node that holds the data (the agent). The server only schedules (jobs table) and records results. If the node is down, the run fails with `node_unavailable` and alert `backup_failed` fires.
- **REQ-BKP-010** (MUST) The control-plane database has a built-in policy (daily, retention default, target chosen at install, `local` by default on the control-plane node, with a UI warning until a remote target is added). It contains encrypted secrets only (the master key is not included).
- **REQ-BKP-011** (SHOULD) Pre/post hooks per policy (one-off commands in the container, for example to flush data).
- **REQ-BKP-100** (MUST) **Restore check:** per policy, on `restore_check_schedule` (default weekly) and after the first successful backup, the server picks the latest snapshot of one target (rotating through targets over time), picks a node with free resources (preferring a node other than the data node), and runs a temporary container to restore it.
- **REQ-BKP-101** (MUST) Checks by type: `postgres` restore with `pg_restore` into a fresh instance, `SELECT 1`, compare the number of user tables and a row estimate with the dump manifest; `mysql`/`mariadb` load and `SELECT 1` plus table count; `mongo` restore and database/collection count; `redis` load RDB and `DBSIZE` vs manifest; `volume` `restic check --read-data-subset=5%`, restore to temp, file count and total size within 1 % of the manifest.
- **REQ-BKP-102** (MUST) Every run stores a manifest (table count, row estimates, file count, size) used by the check. A check result has the list of checks and the duration. Result `passed` or `failed`.
- **REQ-BKP-103** (MUST) A failed check raises alert `restore_check_failed` (critical). A policy with no passed check in 2× its check interval raises `backup_missing`-style warning `restore_check_overdue`.
- **REQ-BKP-104** (MUST) The temp container has no published ports, no mesh network, no outbound internet except to the backup target, memory and CPU limits, and is removed after the check even on failure. The temp data is deleted.
- **REQ-BKP-105** (SHOULD) The UI shows a green "Restore verified N days ago" badge on a service. Not verified shows a warning badge.
- **REQ-BKP-106** (MUST) `polipan backup check <policy>` and the API start a restore check on demand (`POST /backup-policies/{id}/restore-checks`).

## States

Backup run: `queued → running → succeeded | failed`. Restore check: `queued → running → passed | failed`. Policy health (derived): `verified` (last check passed and a backup succeeded since), `unverified`, `failing`.

## Edge cases

- Very large database: the check may exceed the time limit (default 2 h). Result `failed` with `timeout`. The user can set `restore_check_mode: sample` (restore check on `restic check --read-data-subset` only) for huge data.
- No node has free resources: check is `queued` and retried every 15 min, with an alert after 24 h.
- Target unreachable: backup run fails, check uses another target if any.
- Backup of a database during a failover or move: the job waits for the placement to be stable (max 10 min), then runs.
- Restore on the same service while a deploy is active: refused (409).
- Repository password lost (secret destroyed): backups cannot be read. `secret:destroy` on a repo password is blocked while snapshots exist.
- Retention prune and a running restore check on the same snapshot: prune waits (restic lock).

## Touch points

API: `/backup-targets`, `/services/{id}/backup-policies`, `/backup-policies/{id}`, `/backup-policies/{id}/run`, `/backup-policies/{id}/runs`, `/backup-runs/{id}/restore`, `/backup-policies/{id}/restore-checks`, `/restore-checks`. CLI: `polipan backup ...`. Config: `services.<name>.backup`, `backups:` ([07](../07-config-as-code.md)). UI: Service → Backups.

## Acceptance criteria

- **AC-BKP-01** Given a policy with two targets, When a run executes, Then one snapshot exists on each target and the run lists both. (REQ-BKP-001, REQ-BKP-002)
- **AC-BKP-02** Given one of two targets fails, When the run ends, Then the run is `failed`, the other target has its snapshot, and an alert fires. (REQ-BKP-002, REQ-BKP-009)
- **AC-BKP-03** Given a manual run request, When called, Then a run is created and appears in the list. (REQ-BKP-006, REQ-BKP-008)
- **AC-BKP-04** Given a snapshot, When the user restores to a new service, Then a new service contains the data and the original is unchanged. (REQ-BKP-007)
- **AC-BKP-05** Given retention `keep_daily: 7`, When the 8th daily snapshot is made, Then the oldest is removed. (REQ-BKP-005)
- **AC-BKP-06** Given a backup, When the control-plane DB policy exists, Then the DB is backed up daily and the dump has no master key. (REQ-BKP-010)
- **AC-BKP-07** Given a Postgres backup, When the restore check runs, Then a temp container restores it, the checks pass, the result is `passed`, and the container is gone. (REQ-BKP-100, REQ-BKP-101, REQ-BKP-104)
- **AC-BKP-08** Given a corrupted snapshot, When the restore check runs, Then the result is `failed` and a critical alert fires. (REQ-BKP-103)
- **AC-BKP-09** Given a volume policy, When the check runs, Then the file count and size match the manifest within 1 %. (REQ-BKP-101, REQ-BKP-102)
- **AC-BKP-10** Given no passed check in 2× the check interval, When evaluated, Then the UI badge is a warning and an alert exists. (REQ-BKP-103, REQ-BKP-105)

## Out of scope

Point-in-time recovery (WAL archiving) for Postgres in v1. Cross-engine restore. Client-side bandwidth shaping.
