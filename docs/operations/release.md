# Release

This page is for the person who makes a release (the maintainer). A release is **one tag push**. The
release workflow (`.github/workflows/release.yml`) builds, signs, checks and publishes everything.
Nothing in the repository tags, pushes or publishes by itself: you decide when.

## What a release has

| File or image | What it is |
|---|---|
| `polipan_<tag>_linux_amd64.tar.gz`, `..._arm64.tar.gz` | `polipan-server`, `polipan-agent`, `polipan` (CLI), `LICENSE`, `README.md`. Reproducible build. |
| `SHA256SUMS` | SHA-256 of every other file of the release, sorted by name. |
| `SHA256SUMS.sigstore.json` | cosign signature of `SHA256SUMS` (keyless: the certificate names the release workflow and the tag). |
| `polipan_<tag>_source.spdx.json` | SPDX SBOM of the source (Go and npm dependencies). |
| `images.lock` | The three images with their digests. |
| `install.sh`, `install-agent.sh` | The install scripts with the values of this release (digests, checksums). |
| `docker-compose.yml`, `polipan-build.sh` | The files `install.sh` downloads. It checks their checksums. |
| `ghcr.io/<owner>/polipan-server`, `polipan-agent`, `polipan-cli` | Images for amd64 and arm64, tags `<tag>` and `latest` (not for a pre-release). Each is signed by digest and has a signed SPDX SBOM. |

Because the checksum file is signed, one `cosign verify-blob` covers every file, including the install scripts.

## Before the first release (once)

1. **GitHub settings.** Actions are on. In *Settings → Actions → General* keep the default workflow permissions
   at **read**: the workflow asks for more only in the jobs that need it.
2. **No secrets are needed.** Signing is keyless (Sigstore) and uses the OIDC token of the workflow.
   The images go to `ghcr.io` with the built-in `GITHUB_TOKEN`.
3. **Make the packages public** after the first push: *Packages → polipan-server → Package settings → Change visibility*
   (the same for `polipan-agent` and `polipan-cli`), and connect each package to the repository. Without this the install
   script cannot pull the images without a login.
4. **Protect `main` and the tags** (*Settings → Rules*): only maintainers push `v*` tags; `main` needs the CI to pass.
5. **Repository variable `UPGRADE_FROM`** (optional, *Settings → Variables*): the commit or tag of the previous
   pre-release for the upgrade test while no release tag exists. Not needed after the first release.

## Make a release

Use a pre-release first: `v1.0.0-rc.1` runs the whole pipeline and is marked as a pre-release on GitHub.
The next tag (`v1.0.0-rc.2`, then `v1.0.0`) is tested for upgrade against it.

1. Merge all work into `main`. Wait until the CI of `main` is green.
2. Check the repository (it changes nothing):

   ```sh
   git checkout main && git pull
   sh deploy/release/preflight.sh v1.0.0-rc.1
   ```

   It checks the version name, the branch, a clean tree, that `HEAD` is `origin/main`, that the tag is new, and that
   every action in the workflows is pinned by a commit SHA. When it passes it prints the next two commands.
3. Tag and send the tag. This is the step that starts the release:

   ```sh
   git tag -a v1.0.0-rc.1 -m "v1.0.0-rc.1"      # use -s instead of -a if you sign tags with GPG
   git push origin v1.0.0-rc.1
   ```
4. Watch *Actions → release*. The jobs run in this order:
   `prepare` (checks the tag is a version on `main`) → `ci-green` (waits for the CI of the commit) →
   `agent-image` → `images` (server, cli) and `binaries` (amd64, arm64; each is built twice and compared) →
   `upgrade-test` → `release` (collects the files, writes `SHA256SUMS`, signs it, **verifies the whole release**,
   then creates the GitHub release).
   If any job fails, nothing is published on GitHub. Images that were already pushed stay in the registry:
   see *If a release fails* below.
5. Check the result as a user would (next section).
6. For the final release, tag `v1.0.0` the same way. Everything that is not a pre-release also gets the image tag `latest`
   and becomes the target of `releases/latest/download/install.sh`.

## Check a release (as a user)

You need [cosign](https://docs.sigstore.dev/cosign/installation/) 2.x.

```sh
tag=v1.0.0
mkdir polipan-release && cd polipan-release
gh release download "$tag" --repo polipan/polipan          # or download the files by hand
sh path/to/deploy/release/verify.sh --images "$tag" .      # signature, checksums, images and SBOMs
```

`verify.sh` does these steps. You can run them by hand:

```sh
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity "https://github.com/polipan/polipan/.github/workflows/release.yml@refs/tags/$tag" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum -c SHA256SUMS
cosign verify --certificate-identity "https://github.com/polipan/polipan/.github/workflows/release.yml@refs/tags/$tag" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$(sed -n 's/^polipan-server=//p' images.lock)"
```

The install scripts of a release also check what they install:

- `install.sh` uses the images **by digest** (server and agent), downloads `docker-compose.yml` and `polipan-build.sh`
  from the same release and stops if their SHA-256 differs from the value inside the script, and runs `cosign verify` on
  both images when `cosign` is installed. `--require-signature` makes a missing `cosign` an error.
- `install-agent.sh` (the one the panel serves at `/install-agent.sh`) pins the agent image by digest. The server image
  is built with the digest of the agent image of the same release, so a node always gets the agent that belongs to its
  panel. It runs `cosign verify` too when `cosign` is installed.
- A copy from the `main` branch is a development build: it uses the tag `latest`, prints a warning and checks nothing.

Installing a release:

```sh
curl -fsSL https://github.com/polipan/polipan/releases/download/v1.0.0/install.sh | sudo sh -s -- --domain panel.example.com
```

## Build from source and compare

The archive is reproducible: the same commit, the same Go version and the same web build give the same bytes.
The Go builds use `-trimpath`, `-buildvcs=false`, no cgo and fixed times and owners in the archive.

```sh
git clone https://github.com/polipan/polipan && cd polipan && git checkout v1.0.0
make web                                                   # builds the web app (Node 20)
sh deploy/release/build.sh v1.0.0 linux amd64 out
sha256sum out/polipan_v1.0.0_linux_amd64.tar.gz            # compare with SHA256SUMS
```

The images use base images pinned by digest and the build time of the commit (`SOURCE_DATE_EPOCH`).
`make release-test` checks the scripts, the pins and the reproducible build.

## Upgrade test

The new version must start on the database of the previous pre-release, run the new migrations and keep the data.

```sh
make upgrade-test FROM=v1.0.0-rc.1        # a tag or a commit; needs Docker
```

It builds both servers, makes a user, a project, an API token and a session on the old one, starts the new one on the same
database and checks the migrations, `/healthz`, the session, the password login, the API token, the project list and
`GET /audit/verify`. It removes everything it made. The release workflow runs it before publishing; the first release
has nothing to upgrade from and skips it.

## If a release fails

- **A job failed before `release`:** nothing is on GitHub. Fix the cause on `main`, then use the **next** tag
  (`v1.0.0-rc.2`). Do not move a tag.
- **The images were pushed, the release job failed:** the images of that tag exist but have no release. Delete the tag
  (`git push --delete origin <tag>` and `git tag -d <tag>`) and the package versions of that tag in *Packages*,
  then tag again with a new number.
- **A bad release was published:** mark it as a pre-release or delete it (`gh release delete <tag>`), delete the tag,
  and publish a fixed version. Tell users in the release notes. For a security problem follow `SECURITY.md`.

## What the agents did not do

This repository only has the files for the release. No tag was made, nothing was pushed or published, no image was
built for a registry and no signature exists yet. `cosign verify` (AC-OSS-03) can only pass after the first real release.
