# Polipan community forum

A small forum for the Polipan community. One Go program and one PostgreSQL database. No JavaScript is needed
(only the theme button uses a tiny script). No third-party requests.

## What it does

- Categories, topics, replies, pages of 20, search, RSS feed (`/feed.xml`).
- Accounts: name, email (never shown) and password. Passwords use argon2id.
- Roles: member, moderator, admin. The team can lock, pin and delete topics and ban members (`/mod`).
- The category "Announcements" is only for the team.
- Authors can edit or delete their replies in the first 30 minutes. Nobody but the team can delete a first post.
- Posts are plain text. A blank line makes a new paragraph, web addresses become links, and three backticks make a code block.

## Safety

- Sessions are random tokens in an HttpOnly cookie. Only the SHA-256 of the token is stored.
- Every form has a CSRF token. Forms for new accounts, topics and replies also have a hidden field for bots and
  a three-second time check.
- Limits per address and per user: new accounts, tries to log in, topics, posts and searches.
- Strict headers (`Content-Security-Policy` with `default-src 'self'`, no inline script or style).
- All text is escaped. SQL uses parameters only.

## Not in this version

- Password reset by email, and email checks. Email is stored only for the future.
- Private messages, user pages, images and file uploads, likes, notifications.

## Settings (environment variables)

| Name | Meaning | Default |
| --- | --- | --- |
| `DATABASE_URL` | PostgreSQL address. Required. | |
| `FORUM_SECRET` | Random text of 32 characters or more. Required. Keep it. | |
| `FORUM_BASE_URL` | Public address. With `https://` cookies are `Secure` and HSTS is on. | `http://localhost:8080` |
| `FORUM_ADDR` | Listen address. | `:8080` |
| `FORUM_SITE_NAME` | Name in the page title and the feed. | `Polipan Community` |
| `FORUM_MAIN_URL` | Link to the main site. | `https://polipan.dev` |
| `FORUM_DOCS_URL` | Link to the docs. | `https://docs.polipan.dev` |
| `FORUM_FIRST_USER_ADMIN` | `true`: the first account becomes admin. Turn it off after that. | off |
| `FORUM_TRUSTED_PROXY` | `true` behind Polipan or another proxy: the address comes from `X-Forwarded-For`. | off |

## Run it on your laptop

```sh
docker run -d --name forum-db -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=forum -p 127.0.0.1:5432:5432 postgres:17-alpine
docker build -t polipan-forum .
docker run --rm --network host \
  -e DATABASE_URL='postgres://postgres:dev@127.0.0.1:5432/forum?sslmode=disable' \
  -e FORUM_SECRET="$(head -c 24 /dev/urandom | base64)" -e FORUM_FIRST_USER_ADMIN=true \
  polipan-forum
```

Open http://localhost:8080 and register. The first account is the admin.

## Make an admin later

```sh
forum make-admin <username>      # in the container: docker exec <name> forum make-admin <username>
```

## Deploy it with Polipan

1. Make a PostgreSQL database service (for example from the template catalog).
2. Make a service from Git: repository root `forum/`, build with the `Dockerfile`, port `8080`.
3. Set the variables: `DATABASE_URL` from the database service, `FORUM_SECRET` as a secret,
   `FORUM_BASE_URL=https://community.polipan.dev`, `FORUM_TRUSTED_PROXY=true`, and `FORUM_FIRST_USER_ADMIN=true`
   for the first start.
4. Add the domain `community.polipan.dev`.
5. Register the first account, then remove `FORUM_FIRST_USER_ADMIN`.

Back up the database with the Polipan backup policy of the database service. The tables are `users`, `sessions`,
`categories`, `topics` and `posts`.

## Tests

```sh
docker run -d --name forum-test-db -e POSTGRES_PASSWORD=t -e POSTGRES_DB=forum_test -p 127.0.0.1:55439:5432 postgres:17-alpine
docker run --rm --network host -v "$PWD":/src -w /src \
  -e FORUM_TEST_DATABASE_URL='postgres://postgres:t@127.0.0.1:55439/forum_test?sslmode=disable' \
  golang:1.26-bookworm go test ./...
```

The tests erase the schema of the database. The name of the database must contain `test`.

## Licence of the font

JetBrains Mono is under the SIL Open Font License (`static/JetBrainsMono-OFL.txt`).
