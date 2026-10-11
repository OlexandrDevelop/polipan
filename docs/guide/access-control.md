# Access control

## Roles

| Role | Can do |
|---|---|
| owner | Everything, including delete the organization and change owners. |
| admin | Everything in projects and nodes, members (not owners), protected environments. |
| developer | Deploy, edit services and variables. Not protected environments. |
| viewer | Read only. Every write answers `403`. |

A **protected** environment (production by default) can be changed only by admins and owners.
A project can give a user another role in that project. The organization needs at least one owner.

## Login

- Passwords are hashed with argon2id. After 5 failed logins the account is locked for 15 minutes (`429`).
- Two-factor login (TOTP) with recovery codes. An organization can require it.
- Sessions use an HttpOnly cookie, SameSite=Lax and a CSRF header. They end when a member is removed.
- Single sign-on: see [SSO](../operations/sso.md).

## API tokens

Make a token in **Settings → Tokens** or with `polipan token create`. A token has scopes (`read`, `deploy`, `admin`),
an optional project and an expiry. It starts with `pp_`. Only a hash is stored. A token never has more rights than
its user, and it can not make other tokens.

## Audit log

Every change writes an audit event with the actor, the target, a masked diff, the IP address and the request id.
Events are chained with hashes. Check the chain and export the log:

```sh
polipan audit verify
polipan audit export --since 30d > audit.jsonl
```

A push from Git is recorded with actor type `system` and the commit author.
