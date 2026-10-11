# Polipan

Polipan is a self-hosted, open source server panel. It runs apps from Git or from Docker images, databases, domains with automatic HTTPS, secrets, backups and more.

## User guide

- [Install](guide/install.md) and [first steps](guide/first-steps.md)
- [Apps and deploys](guide/apps-and-deploys.md), [domains and HTTPS](guide/domains.md), [variables and secrets](guide/variables-secrets.md)
- [Servers (nodes)](guide/nodes.md), [previews and promotion](guide/previews-promotion.md)
- [Logs, metrics and alerts](guide/monitoring-alerts.md), [access control](guide/access-control.md)
- [CLI](guide/cli.md), [Terraform](guide/terraform.md), [config as code](guide/config-as-code.md)
- [Upgrade guide](guide/upgrade.md) and [troubleshooting](guide/troubleshooting.md)

## Operations and design

- [Specification](spec/README.md): the full design.
- [Roadmap and milestones](spec/14-roadmap-and-milestones.md)
- [Backups and restore checks](operations/backups.md)
- [Databases and templates](operations/databases-and-templates.md)
- [Registry garbage collection](operations/registry-gc.md)
- [Single sign-on (SSO)](operations/sso.md)
- [Release: tag, sign, verify](operations/release.md)
- [Release notes 1.0 (draft)](operations/release-notes-1.0.md)
- [Threat model coverage](operations/threat-model-coverage.md)
- [Performance and chaos report](operations/performance-chaos-report.md)
- [Contributing](https://github.com/polipan/polipan/blob/main/CONTRIBUTING.md)

The docs live in this folder, are versioned with the code, and build with `mkdocs build`.
