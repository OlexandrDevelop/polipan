# 01 – Glossary and Defaults

This file is the single source for names and default numbers. Other files MUST link here and MUST NOT give a different value. If a value changes, change it here only.

## Terms

| Term | Meaning |
|---|---|
| Control plane | The `polipan-server` process with its PostgreSQL, registry, VictoriaMetrics and VictoriaLogs. It holds all state and decisions. |
| Node | A managed server that runs `polipan-agent`, Docker and Caddy. The control-plane host is also a node. |
| Agent | `polipan-agent`. It dials out to the server and applies the desired state on its node. |
| Organization (org) | Top-level tenant. Owns users, nodes, projects, SSO and audit log. |
| Project | A group of environments and services (one product). Holds shared variables and one data key. |
| Environment | A named copy of a project's services. Kinds: `production`, `staging`, `preview`, `custom`. |
| Service | A deployable unit. Types: `app`, `database`, `worker`, `cron`. A template instance expands into services. |
| Release | Immutable record: image digest + config snapshot + secret version refs + source. Created by every deploy. |
| Deployment | One attempt to make a Release live for a service in an environment. Has a strategy and a state. |
| Strategy | How traffic moves: `recreate`, `blue_green`, `canary`. |
| Blue / Green | Blue = the live copy. Green = the new copy started next to it. |
| Placement | The link between a service replica and a node. |
| Ingress node | A node whose Caddy accepts public traffic for a domain. |
| Desired state | What the server wants a node to run. A versioned full document. |
| Actual state | What the agent reports it really runs. |
| Reconcile | The agent loop that makes actual state equal desired state. |
| Protected environment | An environment with `protected: true`. Risky actions need `admin` or higher. Default for `production`. |
| Preview environment | A short-lived environment made for one pull request. |
| Promotion | Creating a Release in a target environment from a Release of a source environment, with no rebuild. |
| Restore check | An automatic test that restores a backup into a temporary container and verifies it. |
| Managed by git | A field set by `polipan.yaml`. The UI shows it read-only. |
| Secret | A write-only value in the secrets store. Never returned by the API. |
| Variable | A plain config value. Can be read back. |
| REQ / US / T / Q / M | Requirement, user story, test scenario, open question, milestone ids. See [README](README.md#id-rules). |

## Defaults

All defaults below can be changed by the user in the place named in the "Set in" column.

| Key | Value | Set in |
|---|---|---|
| `heartbeat_interval` | 5 s | server config |
| `node_suspect_after` | 15 s without heartbeat | server config |
| `node_down_after` | 60 s without heartbeat | server config |
| `join_token_ttl` | 1 h, single use | per token |
| `mesh_cidr` | 10.210.0.0/16, one /24 per node | server config |
| `agent_grpc_port` | 9443 (TCP) | server config |
| `wireguard_port` | 51820 (UDP) | node config |
| `reconcile_interval` | 30 s (also on every new desired-state version) | agent config |
| `drain_seconds` | 30 s | service deploy config |
| `retain_stopped` | 1 stopped old container set kept for fast rollback | service deploy config |
| `retain_releases` | 10 releases keep their image in the registry | project |
| `deploy_strategy` | `blue_green` when the service has an ingress domain or port, else `recreate` | service |
| `canary_steps` | [10, 25, 50, 100] (percent) | service deploy config |
| `canary_step_interval` | 5 min | service deploy config |
| `canary_max_5xx_rate` | 2 % of green requests in the step window | service deploy config |
| `canary_max_p95_ratio` | green p95 ≤ blue p95 × 1.5 | service deploy config |
| `stop_grace_period` | 30 s (SIGTERM, then SIGKILL) | service deploy config |
| `switch_ack_timeout` | 30 s per ingress node | server config |
| `post_switch_check` | 15 s | server config |
| `canary_pause_max` | 24 h, then automatic rollback | service deploy config |
| `failed_green_keep` | 10 min | server config |
| `fast_rollback_target` | < 10 s from click to traffic on old release | design goal |
| `health.type` | `tcp` on the service port when no check is set | service |
| `health.interval` | 5 s | health check |
| `health.timeout` | 3 s | health check |
| `health.healthy_threshold` | 3 consecutive passes | health check |
| `health.unhealthy_threshold` | 3 consecutive fails | health check |
| `health.start_period` | 30 s | health check |
| `health.deploy_timeout` | 300 s for green to become healthy | health check |
| `health.http_expected_status` | 200–399 | health check |
| `build_timeout` | 30 min | project |
| `metrics_retention` | 90 days | server config |
| `logs_retention` | 30 days | server config |
| `metrics_scrape_interval` | 15 s | server config |
| `metrics_raw_retention` | 7 days raw, then downsampled to 5 min until `metrics_retention` | server config |
| `log_buffer_per_node` | 100 MiB on the node disk while the log store is unreachable | agent config |
| `agent_cert_validity` | 30 days, renewed when less than 10 days remain | server config |
| `exec_idle_timeout` | 15 min | server config |
| `failover_target` | service runs on another node within 60 s after the node becomes `down` (stateless) | design goal |
| `soft_delete_purge` | 30 days | server config |
| `audit_retention` | 365 days (older rows are archived as a signed chain segment, never silently dropped) | org |
| `preview_max_active` | 10 per project | project |
| `preview_ttl` | 7 days without a push | project |
| `preview_cleanup_grace` | 1 h after PR close | project |
| `preview_from_forks` | off | project |
| `restore_check_schedule` | weekly (`0 3 * * 0`) | backup policy |
| `backup_retention` | keep-daily 7, keep-weekly 4, keep-monthly 6 | backup policy |
| `cert_expiry_alert` | 14 days before expiry | alert rule |
| `session_ttl` | 7 days sliding, 30 days absolute | org |
| `argon2id` | m = 64 MiB, t = 3, p = 4 | server config |
| `api_rate_limit` | 600 requests per minute per token | server config |
| `idempotency_key_ttl` | 24 h | server config |
| `sse_keepalive` | 15 s | server config |
| `failover` | `none` | service |
| `alert_repeat_interval` | 4 h | alert rule |
| `id_format` | `<prefix>_<ULID>` | fixed |

## Fixed enumerations

Use these exact spellings everywhere (API, DB, UI, config).

| Enum | Values |
|---|---|
| Environment kind | `production`, `staging`, `preview`, `custom` |
| Service type | `app`, `database`, `worker`, `cron` |
| Source type | `git`, `image` |
| Builder | `dockerfile`, `railpack`, `image` |
| Strategy | `recreate`, `blue_green`, `canary` |
| Deployment state | `queued`, `building`, `image_ready`, `starting`, `health_checking`, `shifting`, `switching`, `draining`, `live`, `failed`, `rolled_back`, `cancelled`, `superseded` |
| Node state | `pending`, `ready`, `suspect`, `down`, `draining`, `cordoned`, `maintenance` |
| Role | `owner`, `admin`, `developer`, `viewer` |
| Failover | `none`, `auto` |
| Health check type | `http`, `tcp`, `command` |
| Backup run state | `queued`, `running`, `succeeded`, `failed` |
| Restore check state | `queued`, `running`, `passed`, `failed` |
| Alert severity | `info`, `warning`, `critical` |
| Database engines | `postgres`, `mysql`, `mariadb`, `mongo`, `redis` |

## Id prefixes

`org_`, `prj_`, `env_`, `svc_`, `rel_`, `dep_`, `nod_`, `dom_`, `vol_`, `var_`, `sec_`, `usr_`, `tok_`, `tem_`, `mem_`, `bkp_`, `bkr_`, `rck_`, `alr_`, `chn_`, `aud_`, `sso_`, `git_`, `whk_`, `pol_`, `cmd_`, `team_`, `prr_`, `tpi_`.
