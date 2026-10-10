# C02 – Databases

## Summary

A user creates a managed database service with one click: PostgreSQL, MySQL, MariaDB, MongoDB or Redis. Polipan runs the official image with a volume, generates credentials as secrets, and gives connection strings to other services.

## User stories

- **US-DB-01** As a developer, I create a Postgres database and get a connection string to use in my app.
- **US-DB-02** As a developer, I choose the version and see upgrade warnings.
- **US-DB-03** As a developer, I open a shell or run a query tool inside the database container.
- **US-DB-04** As an admin, I make the database reachable only from services in the same environment, or optionally from the internet on a chosen port.

## Rules

- **REQ-DB-001** (MUST) Supported engines and default image versions: `postgres` 16, `mysql` 8.4, `mariadb` 11, `mongo` 7, `redis` 7. The user can choose other official tags from a supported list per engine.
- **REQ-DB-002** (MUST) A database service is `type = database` with `db_engine` set, one volume `data` with `access_mode = single_writer`, and `deploy_strategy = recreate` (forced, see [F01](F01-zero-downtime-deploy.md)).
- **REQ-DB-003** (MUST) On creation Polipan generates a random password (32 chars) and stores it as a secret `DB_PASSWORD` (plus `DB_USER`, `DB_NAME` as variables). It never shows the password again after creation (write-only). The connection string is shown with the password masked.
- **REQ-DB-004** (MUST) Other services in the same environment can use references `${{db.<name>.URL}}`, `${{db.<name>.HOST}}`, `${{db.<name>.PORT}}`, `${{db.<name>.USER}}`, `${{db.<name>.PASSWORD}}`, `${{db.<name>.NAME}}` in variables. They resolve when a Release is created. The password reference resolves through the secrets store and is treated as a secret.
- **REQ-DB-005** (MUST) By default the database has no public port. The user can expose it with a host port from `30000–39999` on a chosen node. Exposing needs `admin` on protected environments and shows a warning.
- **REQ-DB-006** (MUST) A new database service gets a default backup policy suggestion (daily, retention default) that the user must confirm or skip. Skipping is recorded and the UI shows "no backup" on the service.
- **REQ-DB-007** (MUST) Major version upgrade is never automatic. The UI offers "dump and restore into a new service". A minor tag change is a normal `recreate` deploy.
- **REQ-DB-008** (SHOULD) The UI offers a terminal with the engine client (`psql`, `mysql`, `mongosh`, `redis-cli`) through exec.
- **REQ-DB-009** (MUST) Delete asks for the service name and, if a backup policy exists, offers a final backup. Volumes are deleted with the service only after the user confirms.

## States

Same service-level states as [C01](C01-apps-git-and-image.md#states).

## Edge cases

- Password change: a new secret version is written, the engine password is changed by a one-off command, and dependent services need a redeploy (the UI lists them).
- Node full disk: database fails to start, alert `disk_usage` fires ([F09](F09-alerts-metrics-logs.md)).
- Database on a node that goes `down`: failover rules of [F06](F06-multi-server-failover.md) apply (data loss opt-in).
- Reference to a deleted database: Release creation fails with 422 `unresolved_reference`.

## Touch points

API: services endpoints with `type = database`. CLI: `polipan db create`. Config: `services.<name>` with `db:` block ([07](../07-config-as-code.md)). UI: "Add service → Database".

## Acceptance criteria

- **AC-DB-01** Given the user creates a Postgres service, When it is running, Then a `DB_PASSWORD` secret exists, the API never returns its value, and a second service resolves `${{db.main.URL}}` after a new Release. (REQ-DB-003, REQ-DB-004)
- **AC-DB-02** Given a database service, When the user requests `blue_green`, Then the API returns the forced `recreate` with warning `zero_downtime_not_available`. (REQ-DB-002)
- **AC-DB-03** Given a new database, When created, Then no host port is open and the UI shows a backup suggestion. (REQ-DB-005, REQ-DB-006)
- **AC-DB-04** Given a service references a missing database, When a Release is created, Then it fails with `unresolved_reference` and nothing deploys. (REQ-DB-004)

## Out of scope

Clustering and read replicas. Automatic major upgrades. Database user management UI.
