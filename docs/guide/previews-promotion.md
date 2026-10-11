# Previews and promotion

## Preview environments

Connect a repository and turn on previews for a service. A pull request then gets its own environment with its own
URL, for example `pr-12.preview.example.com`.

- A comment on the pull request shows the URL and the state. A new push updates the same comment.
- Only variables and secrets marked `preview` are given to a preview. Production secrets are never used.
- The preview database is a new empty service. Preview volumes are new volumes, never the ones of the base.
- Pull requests from forks are off by default. A maintainer can allow one with a label and a commit is approved by its sha.
- There is a limit of previews per project, and a time to live. Closing the pull request deletes the preview with its
  containers, volumes and domains.

Set the preview domain in **Settings → Organization**.

## Promote

Promote moves a release from one environment to another, for example staging to production.
The same image digest is used, so nothing is built again. The target keeps its own domains and secrets.

```sh
polipan promote staging production --dry-run
polipan promote staging production --wait
```

The plan shows what changes. Missing variables stop the promotion unless you pass `--allow-missing` or
`--create-missing`. A protected target needs an admin. A promotion of a whole environment deploys the services one
after another and stops at the first failure.
