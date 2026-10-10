# F09 – Alerts, long metrics history, searchable logs — Gap 9

## Summary

Polipan sends alerts to Telegram, Slack, email and webhooks. It keeps metrics for `metrics_retention` and logs for `logs_retention`, and lets users search logs by text and fields. Pipeline and storage: [12-observability](../12-observability.md). Basic charts and tail: [C07](C07-logs-metrics-basic.md).

## User stories

- **US-ALERT-01** As an operator, I get a Telegram message when a server goes down.
- **US-ALERT-02** As a developer, I get a Slack message when my deploy fails or rolls back.
- **US-ALERT-03** As an operator, I get one message, not fifty, and a message when it is resolved.
- **US-OBS-10** As a developer, I look at CPU history for the last 60 days.
- **US-OBS-11** As a developer, I search logs for "timeout" in production in the last 24 h.

## Rules

- **REQ-ALERT-001** (MUST) Channels: `telegram` (bot token secret + chat id), `slack` (incoming webhook URL secret), `email` (SMTP settings, password secret, recipients), `webhook` (URL, optional HMAC secret, JSON body). A channel has a "send test" action.
- **REQ-ALERT-002** (MUST) Built-in rule types: `node_suspect`, `node_down`, `service_unhealthy`, `deployment_failed`, `deployment_rolled_back`, `deployment_live` (info, off by default), `cpu_high`, `memory_high`, `disk_high`, `cert_expiring` (default 14 days), `cert_failed`, `backup_failed`, `restore_check_failed`, `restore_check_overdue`, `backup_missing` (no success in 2× schedule interval), `audit_chain_broken`, `failover_started`, `container_restart_loop` (≥ 5 restarts in 10 min).
- **REQ-ALERT-003** (MUST) Threshold rules (`cpu_high`, `memory_high`, `disk_high`) have `threshold` (percent) and `for` (duration, default 5 min). They are evaluated every 30 s from VictoriaMetrics.
- **REQ-ALERT-004** (MUST) Dedup by fingerprint (rule + target). One `firing` message, repeats every `alert_repeat_interval` (per rule), and one `resolved` message. Instances are stored in `alert_instances`.
- **REQ-ALERT-005** (MUST) Default rules are created for every org at install (node down, service unhealthy, deployment failed, backup failed, restore check failed, cert expiring, disk high 90 %), sent to a channel the first admin picks (or none and a UI banner asks for one).
- **REQ-ALERT-006** (MUST) Rules have a scope (org, project, environment, service, node) and a severity. Channels are chosen per rule. Rules can be muted for a time window (maintenance mute, with reason, audited).
- **REQ-ALERT-007** (MUST) Messages contain: severity, what, where (project/env/service/node), since when, a link to the Polipan page, and (for deploys) the failure reason code and last lines.
- **REQ-ALERT-008** (MUST) Delivery is retried 3 times with back-off. A failing channel raises a UI warning, and never blocks other channels.
- **REQ-ALERT-009** (SHOULD) Platform events (not metric-based) are produced by the server in the same transaction as the state change (no polling).
- **REQ-OBS-010** (MUST) Metrics retention is `metrics_retention` (90 days by default) and can be set up to 1 year. Resolution: raw 15 s data kept for 7 days, then downsampled to 5 min for the rest (VictoriaMetrics downsampling).
- **REQ-OBS-011** (MUST) The UI chart range selector goes up to the configured retention. Charts for services and nodes: CPU, memory, network, disk, requests, 5xx rate, p50/p95/p99 latency.
- **REQ-OBS-012** (MUST) Log retention is `logs_retention` (30 days by default), configurable per org and optionally per project.
- **REQ-OBS-013** (MUST) Log search: free text (word, phrase, regex) and filters on project, environment, service, release, node, level, stream, time range. Results are newest first, paginated by cursor, with highlighted matches and context lines. Max range per query is `logs_retention`. Response in 3 s for 1 GB of data in range.
- **REQ-OBS-014** (SHOULD) Saved searches, and a download of results (up to 100 MB).
- **REQ-OBS-015** (SHOULD) Log-based alert: a rule `log_match` (query + count over window).

## States

Alert instance: `firing → resolved`, re-firing creates a new `firing` with the same fingerprint after a resolve. Channel: `ok | failing`.

## Edge cases

- Node down: one alert for the node, and **not** one for every service on it (child alerts are suppressed while the parent node alert is firing).
- Control plane restarts: firing instances are loaded from the DB, and no duplicate messages are sent.
- VictoriaMetrics down: threshold rules are skipped and a `monitoring_unavailable` warning shows in the UI. Platform-event alerts still work.
- Telegram rate limit: messages are queued and batched (one message with a list when more than 5 in 10 s).
- Retention change: shrinking deletes older data after a confirm. Growing does not recover deleted data.
- Muted rule: the instance still records `firing`, no message is sent.

## Touch points

API: `/alert-channels`, `/alert-rules`, `/alert-instances`, `/services/{id}/logs`, `/logs/search`, `/services/{id}/metrics`, `/nodes/{id}/metrics`. CLI: `polipan alert ...`, `polipan logs search`. Config: `alerts:` block. UI: Alerts, Logs, Metrics pages.

## Acceptance criteria

- **AC-ALERT-01** Given a Telegram channel and the default `node_down` rule, When a node stops its heartbeat for `node_down_after`, Then one Telegram message arrives and a `resolved` message arrives when it returns. (REQ-ALERT-001, REQ-ALERT-004)
- **AC-ALERT-02** Given a firing alert, When it stays firing for 2 × `alert_repeat_interval`, Then exactly 2 reminders are sent. (REQ-ALERT-004)
- **AC-ALERT-03** Given a node down with 5 services on it, When alerts are sent, Then there is 1 node alert and no service alerts for those 5. (edge case rule, REQ-ALERT-004)
- **AC-ALERT-04** Given a failed deployment, When it ends, Then a message with the reason code reaches the channels of the rule. (REQ-ALERT-007)
- **AC-ALERT-05** Given a channel that returns 500, When an alert fires, Then it is retried 3 times and a UI warning shows. (REQ-ALERT-008)
- **AC-OBS-05** Given 60 days of data, When the user selects a 60 day range, Then the chart renders with downsampled points. (REQ-OBS-010, REQ-OBS-011)
- **AC-OBS-06** Given logs from two environments, When the user searches `timeout` with filter `environment=production`, Then only production lines match, newest first, with highlights. (REQ-OBS-013)
- **AC-OBS-07** Given logs older than `logs_retention`, When searched, Then they are not returned. (REQ-OBS-012)

## Out of scope

On-call schedules and paging (PagerDuty/Opsgenie are possible through webhook channels). Tracing. Anomaly detection.
