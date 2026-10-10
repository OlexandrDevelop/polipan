# F11 – Open source and no vendor lock-in — Gap 11

## Summary

Polipan is open source under Apache-2.0. Every component it depends on is open source. Users can leave: all data can be exported, and standard formats are used everywhere (Docker images, Caddy, restic, OCI registry, Prometheus-style metrics).

## User stories

- **US-OSS-01** As a user, I read, build and change the code, and use it in a company.
- **US-OSS-02** As a user, I export my configuration and move to another tool.
- **US-OSS-03** As a contributor, I know how to propose a change.

## Rules

- **REQ-OSS-001** (MUST) The whole repository is licensed Apache-2.0 (`LICENSE`, SPDX headers in source files). No part is closed source, and no feature is locked behind a paid license (no "enterprise edition" in v1). Subject to [Q-01](../15-open-questions.md).
- **REQ-OSS-002** (MUST) Dependencies must have licenses compatible with Apache-2.0 (MIT, BSD, Apache-2.0, MPL-2.0 for unmodified use). A CI check lists dependency licenses and fails on GPL/AGPL/SSPL/BUSL dependencies that are linked into Polipan binaries. External programs run as separate processes or containers (PostgreSQL, Caddy, BuildKit, restic, VictoriaMetrics, VictoriaLogs, registry, WireGuard) are allowed.
- **REQ-OSS-003** (MUST) Open governance files exist: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md` (private report process), `GOVERNANCE.md`, issue and PR templates, DCO sign-off (no CLA).
- **REQ-OSS-004** (MUST) Releases are reproducible and signed: binaries and images signed with cosign (keyless), checksums file, SBOM (SPDX) per release, and a documented build from source.
- **REQ-OSS-005** (MUST) No data is sent to Polipan authors by default. Update checks and anonymous usage stats are **off** by default (opt-in). No license server. The panel works fully offline apart from what the user's apps need.
- **REQ-OSS-006** (MUST) Export and exit paths: `polipan export` (config as `polipan.yaml`), full database dump of the control plane, backup repositories are plain restic repositories (documented restore with restic alone), images are standard OCI images in the built-in registry, Caddy config is standard JSON, metrics use the Prometheus format. No proprietary data format.
- **REQ-OSS-007** (SHOULD) Every external integration sits behind an interface so it can be replaced: git providers, ACME CA, backup targets, alert channels, SSO providers, registry, secret provider.
- **REQ-OSS-008** (MUST) User docs are in the repo (`/docs`), versioned with the code, and published as a static site.
- **REQ-OSS-009** (SHOULD) Public roadmap and milestones in the repo ([14](../14-roadmap-and-milestones.md)). Conventional Commits and semantic versioning.

## Edge cases

- A dependency changes its license: CI blocks it. The dependency is pinned or replaced.
- A contributor adds code with an incompatible license: DCO and license check fail the PR.
- Telemetry request from a user: stays opt-in, a documented list of fields, no personal data.

## Touch points

Repository root files (`LICENSE`, `CONTRIBUTING.md`, ...), [13-testing-and-quality](../13-testing-and-quality.md) (license CI), [02-architecture](../02-architecture.md).

## Acceptance criteria

- **AC-OSS-01** Given the repo, When checked, Then `LICENSE` is Apache-2.0 and source files have SPDX headers. (REQ-OSS-001)
- **AC-OSS-02** Given the CI license job, When a GPL dependency is linked, Then the job fails. (REQ-OSS-002)
- **AC-OSS-03** Given a release, When published, Then signed artifacts, checksums and an SBOM exist and `cosign verify` passes. (REQ-OSS-004)
- **AC-OSS-04** Given a fresh install with default settings, When network traffic is observed, Then no request goes to a Polipan-owned host. (REQ-OSS-005)
- **AC-OSS-05** Given a backup repository, When restored with plain `restic` and the password, Then the data is readable. (REQ-OSS-006)
- **AC-OSS-06** Given the repository, When checked, Then the governance files exist. (REQ-OSS-003)

## Out of scope

A hosted commercial service. Trademark policy (decided with the license question).
