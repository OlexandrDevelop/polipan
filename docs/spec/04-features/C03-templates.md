# C03 – Templates

## Summary

A template is a ready recipe that expands into one or more services (for example WordPress + MySQL, or Plausible + Postgres + ClickHouse). Built-in templates ship with Polipan. Orgs can add custom templates. Templates are validated for safety.

## User stories

- **US-TPL-01** As a user, I pick a template from a catalog, fill in a few inputs, and get running services.
- **US-TPL-02** As an admin, I add my own template from a YAML manifest.
- **US-TPL-03** As a user, I import a Docker Compose file and Polipan converts it to services.

## Rules

- **REQ-TPL-001** (MUST) A template manifest (YAML, validated by [template.schema.json](../schemas/template.schema.json), described in [07](../07-config-as-code.md#7-templates)) has: `slug`, `version`, `name`, `description`, `icon`, `inputs` (name, label, type `string|number|bool|secret|domain`, default, generate), `services` (same shape as config-as-code `services`), `domains`, `volumes`.
- **REQ-TPL-002** (MUST) Inputs of type `secret` are stored as secrets. With `generate: true` Polipan creates a random value and never shows it.
- **REQ-TPL-003** (MUST) Instantiating a template creates a `template_instances` row and its services in one transaction. If any service fails validation nothing is created.
- **REQ-TPL-004** (MUST) Built-in templates live in `/templates` in the repo, are embedded in the server binary and versioned. The v1 catalog contains at least: WordPress, Ghost, Plausible, n8n, Uptime Kuma, Gitea, Nextcloud, Metabase, Directus, Vaultwarden, MinIO, pgAdmin.
- **REQ-TPL-005** (MUST) Every template and imported Compose file passes the safety validator: no `privileged`, no `cap_add` except `NET_BIND_SERVICE`, no `network_mode: host`, no `pid: host`, no `devices`, no Docker socket mount, bind mounts only under `/var/lib/polipan/binds/<service>`. Violations are listed and the instantiation is refused (422 `template_unsafe`).
- **REQ-TPL-006** (MUST) Compose import supports: `image`, `command`, `entrypoint`, `environment`, `ports` (as container ports), `volumes` (named), `depends_on` (as start order hint), `healthcheck`, `restart`, `deploy.resources.limits`. Unsupported keys are listed as warnings and ignored. `build:` is rejected with a message to use a Git service.
- **REQ-TPL-007** (SHOULD) A template update creates a diff against the instance. The user applies it as a normal deploy. Updating never changes secrets or volumes.
- **REQ-TPL-008** (MUST) Deleting a template instance asks whether to delete volumes, and deletes the services as a group.

## Edge cases

- Template needs a domain that is already used: instantiation fails with 409 `domain_in_use` before creating anything.
- Template input of type `domain` with no DNS pointing yet: allowed. HTTPS stays `pending` until DNS works ([C04](C04-domains-https.md)).
- Custom template with the same `slug` and `version`: rejected (409).
- A service of the instance is edited by hand: allowed. The instance diff shows the drift.

## Touch points

API: `/templates`, `/templates/{slug}/instantiate`, `/environments/{id}/import-compose`. CLI: `polipan template list|apply`. UI: Catalog page.

## Acceptance criteria

- **AC-TPL-01** Given the catalog, When a user instantiates WordPress, Then a WordPress service and a MySQL service exist, the DB password is a generated secret, and the app reaches `live`. (REQ-TPL-002, REQ-TPL-003)
- **AC-TPL-02** Given a manifest with `privileged: true`, When instantiated, Then the response is 422 `template_unsafe` and no service is created. (REQ-TPL-005)
- **AC-TPL-03** Given a Compose file with `build:`, When imported, Then it is rejected with a clear message. (REQ-TPL-006)
- **AC-TPL-04** Given two services in a template and the second fails validation, When instantiated, Then neither is created. (REQ-TPL-003)

## Out of scope

A public template marketplace and paid templates.
