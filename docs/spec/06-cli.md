# 06 – CLI

Binary `polipan` (Go, cobra). It uses only the public API ([openapi.yaml](openapi.yaml)). Rules: [F08](04-features/F08-config-as-code-api-cli-terraform.md).

## 1. Global behaviour

| Item | Rule |
|---|---|
| Config file | `~/.config/polipan/config.yaml`: `contexts` (name, url, token), `current_context`. Tokens are stored in the OS keyring when available, else in the file with mode 0600. |
| Env | `POLIPAN_URL`, `POLIPAN_TOKEN`, `POLIPAN_CONTEXT`, `POLIPAN_PROJECT`, `POLIPAN_ENV` override the file. |
| Global flags | `--context`, `--project`, `--env`, `--output table\|json\|yaml` (default `table` on a terminal, `json` when piped), `--no-color`, `--yes` (skip confirms), `--debug` (log requests without secrets). |
| Names | Commands accept a name or an id. A service is addressed as `[project/]env/service` or by `svc_…` id. With `--project` and `--env` set, only the service name is needed. |
| Exit codes | 0 ok, 1 error, 2 usage, 3 not found, 4 permission denied, 5 deployment failed or rolled back (with `--wait`), 6 timeout. |
| Confirm | Destructive commands ask for the name unless `--yes`. |

## 2. Commands

`<x>` is required, `[x]` is optional.

| Group | Command | API operation |
|---|---|---|
| Auth | `polipan login [--url U] [--token T]` (browser device flow if no token), `polipan logout`, `polipan whoami`, `polipan context ls\|use <name>` | `login`, `getMe` |
| Tokens | `polipan token create <name> --scope read,deploy [--project P] [--expires 90d]`, `token ls`, `token rm <id>` | `createToken`, `listTokens`, `revokeToken` |
| Nodes | `polipan node add <name> [--label k=v]` (prints the install command), `node ls`, `node get <n>`, `node label <n> k=v`, `node cordon\|uncordon\|drain <n>`, `node maintenance <n> [--off]`, `node check <n>`, `node rm <n> [--force]` | node operations |
| Projects | `polipan project ls\|create <name>\|get\|rm <name>` | project operations |
| Environments | `polipan env ls\|create <name> [--kind K] [--from E]\|rm <name>\|protect <name> [--off]` | environment operations |
| Services | `polipan service ls\|create\|get\|rm\|stop\|start\|restart\|move <svc> --node N\|exec <svc> [-- cmd]\|health-test <svc>` | service operations |
| Deploy | `polipan deploy <svc> [--commit SHA] [--strategy S] [--drain 30] [--steps 10,25,50,100] [--dry-run] [--wait] [--follow]` | `deployService` |
| Deployments | `polipan deployment ls <svc>\|get <id>\|cancel <id>\|promote <id>\|abort <id>\|pause <id>\|resume <id>\|logs <id>` | deployment operations |
| Releases | `polipan release ls <svc>\|get <id>\|diff <a> <b>\|note <id> "text"` | release operations |
| Rollback | `polipan rollback <svc> [<release>] [--wait]` (default: previous release) | `rollbackService` |
| Promote | `polipan promote <from-env> <to-env> [--service S] [--dry-run] [--allow-missing N,...] [--create-missing] [--wait]` | `promoteRelease`, `promoteEnvironment` |
| Previews | `polipan preview ls\|create --repo R --pr N\|rm <env>` | preview operations |
| Domains | `polipan domain ls <svc>\|add <svc> <host> [--port P]\|rm <host>\|move <host> --to <svc>` | domain operations |
| Volumes | `polipan volume ls <svc>\|add <svc> <name> <path>\|rm <name>` | volume operations |
| Variables | `polipan var ls\|set NAME=VALUE... [--env E] [--service S] [--preview] [--build]\|rm NAME\|import <file.env>` | variable operations |
| Secrets | `polipan secret ls\|set NAME [--from-file F\|--stdin]\|versions NAME\|restore NAME <version>\|rm NAME\|destroy NAME <version>` (value is read from stdin or prompt, never from args) | secret operations |
| Logs | `polipan logs <svc> [-f] [--since 1h]`, `polipan logs search "<text>" [--env E] [--service S] [--level error] [--since 24h]` | log operations |
| Metrics | `polipan metrics <svc>\|--node <n> [--metric cpu,memory] [--since 1h]` (prints a table or sparkline) | metric operations |
| Templates | `polipan template ls\|get <slug>\|apply <slug> --env E [--set k=v ...]\|add <file.yaml>`, `polipan compose import <file> --env E` | template operations |
| Git | `polipan git ls\|add\|rm`, `polipan git repos <conn>` | git operations |
| Backups | `polipan backup target add\|ls\|test\|rm`, `backup policy add\|ls\|get\|rm`, `backup run <policy>`, `backup ls <policy>`, `backup restore <run> --to <svc>\|--new <name>`, `backup check <policy>` | backup operations |
| Alerts | `polipan alert channel add\|ls\|test\|rm`, `alert rule add\|ls\|rm\|mute <id> --for 2h --reason "…"`, `alert ls [--firing]` | alert operations |
| Members | `polipan member ls\|invite <email> --role R\|role <email> <role>\|rm <email>`, `team ls\|create\|add\|rm` | member and team operations |
| SSO | `polipan sso ls\|add\|rm\|map <provider> <group> <role> [--project P]` | SSO operations |
| Audit | `polipan audit ls [--actor A] [--action X] [--since 7d]\|export [--since]\|verify` | audit operations |
| Config | `polipan plan [-f polipan.yaml]`, `polipan apply [-f polipan.yaml] [--prune] [--prune-data] [--yes]`, `polipan export > polipan.yaml`, `polipan validate [-f file]` (local, offline JSON Schema check) | config operations |
| Org | `polipan org get\|set key=value` | org operations |
| Version | `polipan version`, `polipan completion bash\|zsh\|fish` | – |

## 3. Server host commands

Run on the control-plane host with access to the server config and database. They do not use the API.

| Command | Purpose |
|---|---|
| `polipan-server admin reset-password <email>` | Print a one-hour, one-use link to set a new password. |
| `polipan-server admin invite-user <email> [--role r]` | Print a one-hour, one-use invite link for a new user (not owner). |
| `polipan-server admin create-token <email> (--project <id> \| --all-projects) [--scope s] [--expires d]` | Print a new API token once (default `deploy`, 24 h, max 90 days, no `admin` scope). |
| `polipan-server admin revoke-token <token id>` | Revoke an API token. |
| `polipan-server admin remove-user <email>` | Remove a member who is not an owner. |
| `polipan-server admin help` | Print the admin commands. |
| `polipan-server admin rotate-kek` | Re-wrap all project data keys with a new master key. |
| `polipan-server admin rotate-dek --project <id>` | Re-encrypt secrets of one project with a new data key. |
| `polipan-server admin migrate` | Run database migrations (also done at start). |
| `polipan-server admin setup-url` | Print a new one-time setup URL while no owner exists. |

All of them write an audit event with `actor_type = system`.

## 4. Behaviour rules

- **Plan output** of `polipan plan` is stable text: one line per change (`+ create`, `~ update`, `- delete`, `? unmanaged`) with field diffs indented, then a summary line and the plan hash.
- `polipan deploy --follow` prints state changes and log lines from the SSE stream. `--wait` returns the exit code of the final state. Without both, it returns after the `202`.
- `polipan apply` without `--yes` shows the plan and asks to confirm. In a non-interactive shell without `--yes` it fails with exit code 2.
- Secrets are never printed. `polipan secret ls` prints names and versions.

## Requirements

- **REQ-CLI-100** (MUST) Every command in section 2 exists and maps to the named API operations. A test checks that every `operationId` that is not marked UI-only in [13](13-testing-and-quality.md) is reachable from at least one command. The inbound webhooks (`receiveGitWebhook`, `receiveDeployHook`) are called by Git and CI systems, not by users: they are the only operations without a command. The two SSO redirect operations are reached by `polipan sso-login <provider>`, which prints the browser address.
- **REQ-CLI-101** (MUST) Exit codes and the output formats of section 1 are implemented for all commands.
- **REQ-CLI-102** (MUST) Secret values are read only from stdin, a file or a prompt, never from command arguments, and are never written to the CLI log.
- **REQ-CLI-103** (MUST) `polipan validate` works offline with the embedded `polipan.schema.json`.
- **REQ-CLI-104** (SHOULD) The CLI checks the server API version and warns if the CLI is older than the server by more than one minor version.
