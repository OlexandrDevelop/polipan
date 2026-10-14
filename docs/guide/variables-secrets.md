# Variables and secrets

## Variables

A variable is a plain value, for example `PORT=3000`. It is visible in the panel. A variable can live in a project,
an environment or a service. The most specific one wins. A variable can reference another one with `${NAME}`.
Flags: `preview` (also given to preview environments) and `build` (given to the build).

## Secrets

A secret is for passwords and keys. Rules:

- The API never returns the value. You can write a new version, list the versions, restore an old version and
  destroy a version.
- A secret is encrypted with a key of its project. That key is wrapped by the master key in `/etc/polipan/master.key`.
- Values are masked in build logs, deployment logs and audit diffs.
- A release records which versions it used. Rolling back uses those versions again.
- A secret can be a file mount.

With the CLI the value is read from standard input, never from an argument:

```sh
printf '%s' "$VALUE" | polipan secret set DB_PASSWORD --env production
```

## Import a Compose file from Git

When you import a Compose file from a repository, Polipan fills in what a person would type by hand:

- A password or key that the file needs and nobody gave (`${DB_PASSWORD:?...}`, `APP_KEY`, `SECRET_KEY`, `JWT_SECRET`) is generated. It is stored as one secret. Every service that uses the same name gets the same value. You can see it in the Secrets tab.
- If an `env_file` is not in the repository (often it is in `.gitignore`), Polipan reads `.env.example`, `.env.sample` or `.env.dist` from the same folder.
- Host variables like `DB_HOST` and `REDIS_HOST` point to the database or redis service of the file.
- A generated secret in a `command`, an `entrypoint` or a health check is read from the environment of the container (`${NAME}` in `sh -c`).

Variables that cannot be guessed (a token, a DSN) are listed in the warnings. Set them in the Variables tab.

## Rotate keys

On the control-plane host:

```sh
docker compose -f /opt/polipan/docker-compose.yml exec server /polipan-server admin rotate-kek
docker compose -f /opt/polipan/docker-compose.yml exec server /polipan-server admin rotate-dek --project <id>
```

Running services keep running. Each command writes an audit event.
