# Public sites (landing, docs, community)

The Polipan project runs its own public sites on Polipan. They are one project, described in
[`deploy/site/polipan.yaml`](https://github.com/polipan/polipan/blob/main/deploy/site/polipan.yaml).

| Address | What | Source folder |
| --- | --- | --- |
| `polipan.dev` | Landing page in 12 languages | [`site/`](https://github.com/polipan/polipan/tree/main/site) |
| `get.polipan.dev` | Short install address. It sends you to `install.sh` of the latest release. | `site/` (same service) |
| `docs.polipan.dev` | This documentation | `docs.Dockerfile` |
| `community.polipan.dev` | A small forum | [`forum/`](https://github.com/polipan/polipan/tree/main/forum) |

## Set it up

1. Install Polipan on a server with a domain (see [Install](../guide/install.md)).
2. Point four DNS A records (`polipan.dev`, `get`, `docs`, `community`) to the server.
3. Make the project `polipan-site` and the secret `FORUM_SECRET` (random, 32 characters or more).
4. Connect the Git repository, then run `polipan apply -f deploy/site/polipan.yaml --yes`.
5. Open `community.polipan.dev`, register the first account and run
   `forum make-admin <name>` in the forum container (or start once with `FORUM_FIRST_USER_ADMIN=true`).

## Change the domain or the repository

- Landing and short address: `site/site.config.json` and the container variables `SITE_GET_HOST` and `SITE_REPO`.
- Forum: the `FORUM_*` variables in the file above.
- The install script and the cloud-init file use `https://get.polipan.dev`. Change it in
  `deploy/cloud-init/polipan.yaml` and `deploy/install.sh` if you use another domain.

## The short install address needs a release

`get.polipan.dev` redirects to `releases/latest/download/install.sh` of the GitHub repository. It works only after
the first release (see [Release](release.md)).
