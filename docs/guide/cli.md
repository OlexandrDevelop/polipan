# Command line (CLI)

The `polipan` binary uses only the public API. Everything you can do in the panel you can do with the CLI.

## Log in

```sh
polipan login --url https://panel.example.com --token pp_xxxxxxxx
polipan whoami
```

The config is in `~/.config/polipan/config.yaml` (contexts with url and token, mode 0600). `POLIPAN_URL`,
`POLIPAN_TOKEN`, `POLIPAN_CONTEXT`, `POLIPAN_PROJECT` and `POLIPAN_ENV` override it. Use `polipan context ls|use <name>`
for more than one panel.

## Global flags

`--context`, `--project`, `--env`, `--output table|json|yaml` (default `table` in a terminal, `json` in a pipe),
`--no-color`, `--yes` (skip confirmations), `--debug` (log requests, no secrets).

A service is written `[project/]env/service` or as its id. With `--project` and `--env` the name is enough.

## Common tasks

```sh
polipan project create blog
polipan service ls
polipan deploy blog/staging/web --wait --follow
polipan rollback blog/production/web --wait
polipan promote staging production --dry-run
polipan domain add web blog.example.com --port 3000
polipan var set PORT=3000 --env staging
printf '%s' "$PASS" | polipan secret set DB_PASSWORD --env production
polipan logs web -f
polipan backup run nightly
polipan audit verify
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | OK |
| 1 | Error |
| 2 | Wrong use (for example `apply` without `--yes` in a script) |
| 3 | Not found |
| 4 | Permission denied |
| 5 | Deployment failed or rolled back (with `--wait`) |
| 6 | Timeout |

Secrets are read only from standard input, a file or a prompt, never from an argument.

## Server host commands

On the control-plane host, `polipan-server admin` has `reset-password <email>`, `rotate-kek`, `rotate-dek --project <id>`,
`migrate` and `setup-url`. They do not use the API and write an audit event.

The full list of commands is in the [CLI specification](../spec/06-cli.md). `polipan completion bash|zsh|fish` makes
shell completion.
