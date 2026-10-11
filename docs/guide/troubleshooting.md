# Troubleshooting

| Problem | What to check |
|---|---|
| Certificate stays `pending` or is `failed` | The DNS record must point to an ingress node. Ports 80 and 443 must be open. The error text is in the domain panel. |
| Deployment `failed` in `health_checking` | The port or the check path is wrong, or the app is too slow to start: raise `start_period`. The old release keeps serving. |
| Node `suspect` or `down` | Check the agent: `systemctl status polipan-agent`. Check that port 9443 reaches the control plane and the clock is right. |
| Login says `429` | Five failed logins lock the account for 15 minutes. |
| Secrets do not decrypt after a restore | The master key file must be the one used before. |
| `409 managed_by_git` | The value is set by `polipan.yaml`. Change the file. |
| `403 protected_environment` | Ask an admin. |
| No metrics or logs | `victoria_metrics_url` and `victoria_logs_url` must be set, and the agent must reach the ingest address (port 9444). |

Server logs: `docker compose -f /opt/polipan/docker-compose.yml logs -f server`. Every error answer has a `request_id`
that is also in the logs.

## Reporting a security problem

See [SECURITY.md](https://github.com/polipan/polipan/blob/main/SECURITY.md). Do not open a public issue.
