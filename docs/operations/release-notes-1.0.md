# Polipan 1.0 release notes (draft)

This is a draft for the project owner. Nothing is tagged or published.

## Highlights

- Single-node and multi-node deploys, previews, promotion, backups, alerts, SSO, config as code, CLI and Terraform provider.
- Hardening pass (M10b): threat model tests, fuzz tests, performance and chaos reports, dependency scan in CI.
- New operations: `deleteOrg` and `moveDomain`.

## Upgrade

See the [upgrade guide](../guide/upgrade.md). Migrations run at server start. Back up the database first.

## Accepted risks

These risks are known and accepted for 1.0. Each one is linked from the
[threat model coverage](threat-model-coverage.md).

| Id | Risk | Why it is accepted |
|---|---|---|
| AR-1 | The internal DNS name `<service>.<env>.polipan.internal` can be resolved by containers of other environments on the same node. The firewall blocks the traffic, so the name only tells that a service exists. (Open remark R4 of M7.) | Names leak no data and no traffic. Planned: answer DNS per source environment. The short name of a service (`app`) resolves only through the search domain of the own environment (`e-<env id>`), so it leaks nothing across environments. |
| AR-2 | `NET_RAW` is dropped from every workload container, so `ping` and other raw ICMP tools do not work inside a container. | Safer default (no ARP or packet spoofing). Apps that need it can use TCP checks. |
| AR-3 | The Caddy admin socket has mode 0660 inside a 0700 directory owned by the agent, not 0600. Caddy runs as another user in its container and needs group access. | The directory is the barrier: only root and the agent can enter it. It is never on a TCP port. |
| AR-4 | Root on a node sees the secrets of the workloads placed on this node. | Known limit in the threat model: a node only gets its own workloads. |
| AR-5 | The `preview` node label is optional. Without it, previews can run on any node. | Previews still get only `preview = true` values and never mount production volumes. |
| AR-6 | The live build isolation test (`test/e2e/sec06.sh`) needs root, Docker and nftables. It was not run in the hardening sandbox. Unit tests of the compose file and the nftables rules pass. | Run `sudo test/e2e/sec06.sh` before tagging. |
| AR-7 | Cosign keyless signing, the SBOM and the checksum check of the installer can only be proven on a real release. `make release-test` checks the scripts and the reproducible build. | The first tag will run `release.yml`. Check the signature by hand once. |

## Dependency scan

- `govulncheck` (server, agent, CLI, Terraform provider): no reachable vulnerability. The Go toolchain moved to 1.26
  and `x/net`, `x/crypto`, `x/text`, `grpc` and `pgx` were updated for this.
- `npm audit` for the runtime packages of the web app: no finding.
- AR-8: `npm audit` reports 5 high and 2 moderate findings in development tools only (`tailwindcss`, `chokidar`,
  `braces`, `micromatch`, `fast-glob`, `postcss-*`). They run at build time on trusted input and are not shipped.
  The CI gate checks the runtime packages (`--omit=dev`). Fix with the next Tailwind major upgrade.
