# Polipan site

The public landing page of Polipan. It is a static site in 12 languages. It has no packages and no third-party requests.

## Build and run

```sh
cd site
node build.mjs                      # writes dist/ (Node 20)
docker build -t polipan-site .
docker run --rm -p 127.0.0.1:8080:8080 polipan-site
```

## Change the domain or the repository

Everything is in `site.config.json`: the domain, the short install host, the docs and community addresses, the
GitHub repository and the install command. The `get` host and the repository are also environment variables of
the container: `SITE_GET_HOST` and `SITE_REPO`.

## Short install address

The container answers on two kinds of host names:

- `get.polipan.dev` (`SITE_GET_HOST`): `/` and `/install.sh` send you to the `install.sh` of the latest release.
  `/install-agent.sh` does the same for the agent.
- Any other host name: the site.

`/healthz` answers `ok` on every host.

## Languages

The texts are in `i18n/<code>.json`. English is `i18n/en.json`. The build stops when a language has a missing or
extra key. To add a language, add it to `languages` in `site.config.json` and add its file.

## Deploy with Polipan

Make a service from Git: repository root `site/`, build with the `Dockerfile`, port `8080`. Add two domains:
`polipan.dev` and `get.polipan.dev`. Both go to the same service.

## Licence of the font

JetBrains Mono is under the SIL Open Font License. The text is in `assets/JetBrainsMono-OFL.txt`.
