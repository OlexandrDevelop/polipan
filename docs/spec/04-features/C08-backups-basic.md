# C08 – Backups (basic)

This core feature is delivered by [F10](F10-backups-and-restore-checks.md). Easypanel-level backup (scheduled database and volume backup to S3, manual backup and restore) is the first part of F10. This file only lists the basic user stories and points to the requirements. It defines no requirements of its own.

## User stories

- **US-BKP-01** As a developer, I set a daily backup of my database to an S3 bucket.
- **US-BKP-02** As a developer, I start a backup by hand and download or restore it.
- **US-BKP-03** As an admin, I see the list of backups with size and age.

## Covered by

| Need | Requirement |
|---|---|
| Policy with schedule, target, retention | REQ-BKP-001, REQ-BKP-002 |
| Manual run and restore | REQ-BKP-006, REQ-BKP-007 |
| Backup list and status | REQ-BKP-008 |

## Acceptance criteria

- **AC-C08-01** See AC-BKP-01 to AC-BKP-06 in [F10](F10-backups-and-restore-checks.md#acceptance-criteria). C08 is accepted when they pass.
