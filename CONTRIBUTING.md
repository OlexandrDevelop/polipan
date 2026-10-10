# Contributing to Polipan

Thank you for your help. This file tells you how to send a change.

## Before you start

- Read `docs/spec/README.md`. The spec is the design.
- For a big change, open an issue first.
- Be kind. See `CODE_OF_CONDUCT.md`.

## Set up

You need Go 1.23+, Node.js 20+ and Docker.

Run `make help` to see all targets. The main ones:

- `make build`: build server, agent, cli and web.
- `make lint`: lint and format check.
- `make gates`: run the CI gates that work offline.

Tests that need PostgreSQL read the URL from `POLIPAN_TEST_DATABASE_URL`.

## Commits

- Use Conventional Commits: `type(scope): short description`.
- Types: feat, fix, refactor, docs, test, chore, perf, style, build, ci.
- Subject: max 50 characters, lowercase, no period, imperative mood.
- Version numbers follow semantic versioning.
- Every milestone of `docs/spec/14-roadmap-and-milestones.md` ends with a tagged pre-release.

## DCO sign-off (no CLA)

Every commit needs a sign-off line. It says you have the right to send the code under the project license.

Use `git commit -s`. It adds `Signed-off-by: Your Name <you@example.com>`. CI checks it.

## Rules for code

- Every source file starts with `SPDX-License-Identifier: Apache-2.0`.
- Do not add dependencies with GPL, AGPL, SSPL or BUSL licenses.
- `api/openapi.yaml` is a copy of `docs/spec/openapi.yaml`. Change the spec file, then run `make api-sync`.
- `proto/agent.proto` is a copy of `docs/spec/schemas/agent.proto`. Same rule.
- Run `make generate` after a change in the API or proto file. CI fails if generated code is old.
- Write tests with the code. Name each test with its `AC-` or `REQ-` id.

## Pull requests

- Fill in the PR template.
- Keep a PR small and focused.
- CI must pass.
