# Logs, metrics and alerts

## Logs

Container logs are sent by the agent, stored with the labels of org, project, environment, service and node, and
shown live in the **Logs** tab. A node that was offline sends the lines it kept when it comes back.
Search works on one service or on the whole environment:

```sh
polipan logs web -f
polipan logs search "timeout" --env production --level error --since 24h
```

The search text is always sent as a quoted string, so it can not change the query. Logs older than `logs_retention`
(30 days by default, set per organization) are not returned.

## Metrics

Per service and per node: CPU, memory, network and disk. Charts choose the step by the range, so a chart has at most
about 1500 points. Retention is set in **Settings → Organization**.

## Alerts

Add a **channel** (email, Telegram, Slack or a webhook), then **rules**. Default rules cover: node down, service unhealthy,
deployment failed, backup failed, restore check failed, certificate expiring and disk high. A rule has no channel until you make the first one.
A firing alert sends a message, up to two reminders, and a message when it is resolved. A node-down alert hides the
alerts of the services on that node. Mute a rule for a time with a reason:

```sh
polipan alert rule mute <id> --for 2h --reason "maintenance"
```

A channel that fails is tried three times. Webhook URLs are checked against the SSRF deny list.
