# Polipan

Self-hosted, open source server panel (Apache-2.0). Spec: [docs/spec/README.md](docs/spec/README.md).

## Install

On a Linux server with systemd, as root:

```sh
curl -fsSL https://raw.githubusercontent.com/OlexandrDevelop/polipan/main/deploy/install.sh | sudo sh -s -- --domain panel.example.com
```

Leave out `--domain` to test over plain HTTP on the server address. More: [docs/guide/install.md](docs/guide/install.md).

## Repository

| Folder | Content |
|---|---|
| `server` | `polipan-server` (Go): API, job queue, web app inside |
| `agent` | `polipan-agent` (Go) |
| `cli` | `polipan` command line client (Go) |
| `web` | React web app, embedded in the server binary |
| `api` | `openapi.yaml` and generated Go and TypeScript code |
| `proto` | `agent.proto` and generated gRPC code |
| `deploy` | Dockerfiles, installer and compose files |
| `docs` | The spec and user docs |
| `test` | CI scripts, fixtures and e2e suites |

## Build

You need Go 1.26+, Node.js 20+ and Docker. Run `make help` for all targets.

```sh
make build     # web app, then server, agent and cli into ./bin
make images    # Docker images
make gates     # CI gates that need no database
```

Run the server: set `POLIPAN_DATABASE_URL` to a PostgreSQL 16 database and start `bin/<os>-<arch>/polipan-server`.
It applies the migrations, then listens on `:8080` (`/healthz`, `/api/v1`, `/api/docs`).

See [CONTRIBUTING.md](CONTRIBUTING.md) to send a change.
