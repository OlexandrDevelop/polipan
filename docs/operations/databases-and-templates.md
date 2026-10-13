# Databases and templates

## Databases

**Add service → Database** makes a service from the official image of PostgreSQL 16, MySQL 8.4, MariaDB 11, MongoDB 7 or Redis 7
(other versions of the engines can be chosen). It has one volume `data` with one writer and always deploys with `recreate`
(there is a short downtime on every deploy). A password of 32 characters is made as the secret `DB_PASSWORD`. It is never shown.
`DB_USER` and `DB_NAME` are variables. There is no public port. Other services of the same environment connect to it.

Use references in the variables of another service. They resolve when a release is made:

| Reference | Value |
|---|---|
| `${{db.<name>.URL}}` | Connection string. The password is filled in at release time and stays a secret. |
| `${{db.<name>.HOST}}`, `PORT`, `USER`, `NAME` | The parts of it |
| `${{db.<name>.PASSWORD}}` | The password (a secret) |

A reference to a database that does not exist fails the release with `unresolved_reference`. Nothing is deployed.

A minor version change (for example `16.4` to `16.6`) is a normal deploy. A **major** change is refused: dump and restore into a
new service (see [backups](backups.md)). To open the database to the internet, set `host_port` (30000 to 39999) on a node. On a protected
environment this needs an admin. Ports 22, 53, 80, 443, 9443 and the ports of the panel are never allowed.

## Templates

**Catalog** has 13 built-in templates (WordPress, Ghost, Plausible, n8n, Uptime Kuma, Gitea, Nextcloud, Metabase, Directus, Vaultwarden, MinIO, pgAdmin, Laravel).
A template makes all its services together or none. The manifests are in `/templates` of the repository and are built into the server.

Custom templates (YAML, see `docs/spec/schemas/template.schema.json`) are added in the catalog. Inside a template:
`${{inputs.NAME}}` reads an input, `${{services.NAME.host}}` the host of another service of the template, and `${{db.NAME.URL}}`
the database references above. An input of type `secret` is a secret of the service that uses it. With `generate: true` Polipan makes the value.

Every template and every imported Compose file must pass the safety check: no `privileged`, no `cap_add` except `NET_BIND_SERVICE`,
no host network or pid, no `devices`, no Docker socket, and bind mounts only under `/var/lib/polipan/binds/<service>`.
Compose import supports `image`, `command`, `entrypoint`, `environment`, `ports` (the container port), named `volumes`, `depends_on`,
`healthcheck`, `restart` and `deploy.resources.limits`. `build:` is refused: use a Git service.
