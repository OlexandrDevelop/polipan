# 15 – Open Questions for the User

These are product questions only. **No technology choice is open.** The stack in [02-architecture](02-architecture.md) is final. Each question has a **default**. Build agents MUST use the default until the user answers. If the user changes an answer, the Architect updates the files listed in "Affects".

| Id | Question | Default (used now) | Affects |
|---|---|---|---|
| Q-01 | License: Apache-2.0 (permissive, allows closed forks) or AGPL-3.0 (forces forks to share changes)? | Apache-2.0 | [F11](04-features/F11-open-source.md), `LICENSE`, [REQ-OSS-001](04-features/F11-open-source.md#rules) |
| Q-02 | Final product name, domain and trademark. The spec uses the name "Polipan" and placeholder domains `polipan.dev` (schema ids, install URL) and the Terraform namespace `polipan/polipan`. | Keep "Polipan" and `polipan.dev` | [02](02-architecture.md#8-installation), [schemas](schemas/polipan.schema.json), [F08](04-features/F08-config-as-code-api-cli-terraform.md) |
| Q-03 | Should v1 manage DNS records automatically (Cloudflare, Route53, Hetzner)? | No. The user creates DNS records. The UI shows the exact records. DNS-01 providers are used only for wildcard certificates. | [C04](04-features/C04-domains-https.md), [09](09-multi-server.md#8-ingress-with-many-nodes) |
| Q-04 | Does the control plane need high availability after 1.0 (two server instances and a PostgreSQL replica)? | No HA in 1.x. Backups of the control-plane DB and a documented manual restore. | [00](00-vision-and-scope.md), [09](09-multi-server.md#10-control-plane-host) |
| Q-05 | Should production deploys need an approval from a second person? | Not in v1. Protected environments only restrict who can deploy (admin and above). | [F07](04-features/F07-teams-rbac-audit-sso-secrets.md), [10](10-security.md#3-roles-and-permissions) |
| Q-06 | When should external secret managers (Vault, OpenBao, cloud KMS) be added? | After 1.0. The built-in store is the only backend in v1. | [10](10-security.md#4-secrets), [14](14-roadmap-and-milestones.md#4-after-10-not-part-of-v1) |
| Q-07 | Which Git providers must work at 1.0? | GitHub (GitHub App), GitLab and Gitea/Forgejo (token + webhook). Bitbucket later. | [C01](04-features/C01-apps-git-and-image.md), [F03](04-features/F03-preview-environments.md) |
| Q-08 | Anonymous usage statistics and update checks? | Both off by default and opt-in only. | [F11](04-features/F11-open-source.md) |
| Q-09 | UI languages at 1.0? | English only. Strings go through an i18n layer. | [11](11-ui.md) |
| Q-10 | Builds on worker nodes and multi-architecture images at 1.0? | No. Builds run on the control-plane host for its CPU architecture. A service pinned to a node with another architecture needs a multi-arch image from the user. | [C01](04-features/C01-apps-git-and-image.md), [14](14-roadmap-and-milestones.md) |
