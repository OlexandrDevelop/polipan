# Config as code (`polipan.yaml`)

Keep the setup of a project in a file in your repository. Polipan reads it, shows a plan, and applies it.

```yaml
version: 1
project: blog
services:
  web:
    source:
      git: { repo: acme/blog, branch: main, builder: railpack }
    ports: [3000]
    domains:
      - host: blog.example.com
        port: 3000
    healthcheck: { type: http, path: /healthz }
    resources: { cpu: "500m", memory: 512Mi }
```

## Commands

```sh
polipan validate -f polipan.yaml   # offline check with the JSON Schema
polipan plan -f polipan.yaml       # + create, ~ update, - delete, ? unmanaged
polipan apply -f polipan.yaml --yes
polipan export > polipan.yaml      # from the live state, without secret values
```

The plan has a hash. `apply` applies exactly that plan, and a second `apply` changes nothing.
An unknown key is an error with the line and the field name. Secrets are never in the file: refer to them by name.

## Git sync

Link a project to a repository file in **Project → Config source** (admin only). A push updates the project. A push
for a protected environment waits as a pending plan until an admin approves it.

## What is managed

A field set by the file is marked `managed_by_git`. The panel shows it as read only, and the API answers
`409 managed_by_git`. A field edited by hand and different from the file is shown as drift. Objects that are not in
the file are `unmanaged` and are removed only with `--prune` (data only with `--prune-data`).

See the [specification](../spec/07-config-as-code.md) for all keys and more examples.
