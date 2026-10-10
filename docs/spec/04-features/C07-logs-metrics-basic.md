# C07 – Logs and metrics (basic view)

## Summary

Every service has a live log view and basic charts (CPU, memory, network, disk, requests). Long history, search and alerts are in [F09](F09-alerts-metrics-logs.md). The pipeline is in [12-observability](../12-observability.md).

## User stories

- **US-OBS-01** As a developer, I tail the logs of my service live.
- **US-OBS-02** As a developer, I see CPU and memory of my service for the last hour.
- **US-OBS-03** As an admin, I see CPU, memory and disk of every node.

## Rules

- **REQ-OBS-001** (MUST) The agent reads container stdout/stderr and ships lines to VictoriaLogs with fields: `org`, `project`, `environment`, `service`, `release`, `node`, `container`, `stream`, `level` (guessed from JSON `level` or common prefixes, else `unknown`), `time`.
- **REQ-OBS-002** (MUST) Live tail: `GET /services/{id}/logs/stream` (SSE) sends the last 200 lines, then new ones. Multiple replicas are merged by time and labeled.
- **REQ-OBS-003** (MUST) Container metrics per service: CPU, memory (usage and limit), network in/out, block I/O, restarts. Node metrics: CPU, memory, disk, load, container count. Scrape interval 15 s.
- **REQ-OBS-004** (MUST) Caddy metrics per domain: requests, status classes, latency histogram. They are labeled with `service` and `release` so canary and blue-green can compare versions.
- **REQ-OBS-005** (MUST) The default chart range is 1 h and selectable up to `metrics_retention`.
- **REQ-OBS-006** (MUST) The log view works while a node is `suspect`. Lines buffered on an offline agent (up to 100 MiB per node, disk) are sent when it reconnects.
- **REQ-OBS-007** (MUST) Deployment logs (build, pre_deploy) are separate from runtime logs and are shown on the deployment page.

## Edge cases

- Container prints a very long line: cut at 64 KiB with a marker.
- Log volume spike: per-service rate limit 5000 lines/s, dropped lines are counted and shown ("N lines dropped").
- VictoriaLogs down: live tail falls back to `docker logs` through the agent (last 1000 lines).

## Touch points

API: `/services/{id}/logs`, `/services/{id}/logs/stream`, `/services/{id}/metrics`, `/nodes/{id}/metrics`. CLI: `polipan logs`. UI: Service → Logs, Metrics.

## Acceptance criteria

- **AC-OBS-01** Given a running service, When the user opens Logs, Then new lines appear within 3 s of being written. (REQ-OBS-002)
- **AC-OBS-02** Given a replica on each of two nodes, When logs are tailed, Then lines from both appear with the node label. (REQ-OBS-002)
- **AC-OBS-03** Given metrics were collected for 2 h, When the user selects "last 1 h", Then the chart has data points at about 15 s resolution. (REQ-OBS-003, REQ-OBS-005)
- **AC-OBS-04** Given a node was offline for 5 min, When it reconnects, Then logs written during the gap appear. (REQ-OBS-006)

## Out of scope

Tracing (OpenTelemetry traces) in v1.
