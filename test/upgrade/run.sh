#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Upgrade test (M10, done criterion 6 of docs/spec/14): the new version starts on the database of the
# previous pre-release, runs the new migrations and keeps the data and the logins.
#
#   sh test/upgrade/run.sh [--from REF] [--to REF]
#
# --from  the previous pre-release: a tag or a commit (default: the newest v* tag before --to, else the
#         commit in POLIPAN_UPGRADE_FROM; the script stops if it has none)
# --to    the version to test (default: HEAD)
#
# What it does: builds the server of both refs (Go in a container, no web app: the test is about the
# database and the API), starts PostgreSQL and the old server, makes a first user, a project, an API
# token and a session, stops the old server, starts the new one on the same database and checks
#   - the new migrations ran and /healthz is ok,
#   - the session, the password login and the API token still work,
#   - the project is still there and the audit chain is still valid (verifyAuditChain).
# Everything it makes (containers, network, images, folders, volume) is removed at the end, and it
# prints the free disk space before and after.
# Optional: POLIPAN_UPGRADE_GOMODCACHE=DIR uses that Go module cache (then the build needs no network).
# POLIPAN_UPGRADE_GO_IMAGE=REF uses another Go image for the build.
set -eu

from=""
to="HEAD"
while [ $# -gt 0 ]; do
  case "$1" in
    --from) from=$2; shift 2 ;;
    --to) to=$2; shift 2 ;;
    -h|--help) sed -n '3,22p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
say() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

[ -n "$from" ] || from=$(git describe --tags --abbrev=0 --match 'v*' "$to^" 2>/dev/null || true)
[ -n "$from" ] || from=${POLIPAN_UPGRADE_FROM:-}
[ -n "$from" ] || fail "no previous pre-release: give --from REF (a tag or a commit)"
git rev-parse -q --verify "$from^{commit}" >/dev/null || fail "unknown ref: $from"
git rev-parse -q --verify "$to^{commit}" >/dev/null || fail "unknown ref: $to"

# The Go image is pinned by digest, like the build images of the release.
go_image=${POLIPAN_UPGRADE_GO_IMAGE:-golang:1.26-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c}
run_id=$$
work=$(mktemp -d)
net="pp-upgrade-$run_id"
pg="pp-upgrade-pg-$run_id"
old="pp-upgrade-old-$run_id"
new="pp-upgrade-new-$run_id"
cache_volume="pp-upgrade-gomod-$run_id"
free_kb() { df -Pk "$root" | awk 'NR==2{print $4}'; }
free_before=$(free_kb)

cleanup() {
  docker rm -fv "$old" "$new" "$pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  docker volume rm "$cache_volume" >/dev/null 2>&1 || true
  rm -rf "$work"
  free_after=$(df -Pk "$root" | awk 'NR==2{print $4}')
  say "Cleaned up. Free disk space before: $((free_before / 1024)) MB, after: $((free_after / 1024)) MB."
}
trap cleanup EXIT

# ---- Build both servers ----
build() { # build REF NAME
  mkdir -p "$work/$2/src" "$work/$2/bin"
  git archive "$1" | tar -x -C "$work/$2/src"
  if [ -n "${POLIPAN_UPGRADE_GOMODCACHE:-}" ]; then
    mod="-v $POLIPAN_UPGRADE_GOMODCACHE:/go/pkg/mod -e GOPROXY=off"
  else
    mod="-v $cache_volume:/go/pkg/mod"
  fi
  # shellcheck disable=SC2086
  docker run --rm --user "$(id -u):$(id -g)" $mod -v "$work/$2/src:/src" -v "$work/$2/bin:/out" -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/gocache -e HOME=/tmp "$go_image" \
    go build -trimpath -o /out/polipan-server ./server/cmd/polipan-server
}
if [ -z "${POLIPAN_UPGRADE_GOMODCACHE:-}" ]; then
  docker volume create "$cache_volume" >/dev/null
  # A new volume belongs to root, but the build runs as the current user.
  docker run --rm -v "$cache_volume:/m" alpine:3.20 chmod 0777 /m
fi
say "Building the old server ($from)"
build "$from" old
say "Building the new server ($to)"
build "$to" new

# ---- Database ----
docker network create "$net" >/dev/null
docker run -d --name "$pg" --network "$net" -e POSTGRES_PASSWORD=pw -e POSTGRES_USER=polipan -e POSTGRES_DB=polipan \
  postgres:16-alpine >/dev/null
i=0
until docker exec "$pg" pg_isready -U polipan -d polipan >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || fail "PostgreSQL did not start"; sleep 1
done
sleep 2
psql() { docker exec "$pg" psql -U polipan -d polipan -tA -c "$1"; }

master_key=$(head -c 32 /dev/urandom | base64)
start_server() { # start_server NAME BINARYDIR
  docker run -d --name "$1" --network "$net" -p 127.0.0.1::8080 \
    -e "POLIPAN_DATABASE_URL=postgres://polipan:pw@$pg:5432/polipan?sslmode=disable" \
    -e "POLIPAN_MASTER_KEY=$master_key" -e POLIPAN_EXTERNAL_URL=http://127.0.0.1 \
    -v "$2:/app:ro" alpine:3.20 /app/polipan-server >/dev/null
  i=0
  until docker exec "$1" /app/polipan-server healthcheck >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then docker logs "$1" 2>&1 | tail -20; fail "the server $1 did not become healthy"; fi
    sleep 1
  done
  port=$(docker port "$1" 8080/tcp | head -n 1 | sed 's/.*://')
  base="http://127.0.0.1:$port"
}

# ---- The old version: make the data ----
say "Starting the old server and making data"
start_server "$old" "$work/old/bin"
migrations_old=$(psql "SELECT count(*) FROM schema_migrations")
jar="$work/cookies"
hdr="$work/headers"
pw="correct horse battery staple"
setup_token=$(docker exec "$old" /app/polipan-server setup-url | sed 's/.*token=//')
curl -fsS -c "$jar" -D "$hdr" -H 'Content-Type: application/json' \
  -d "{\"setup_token\":\"$setup_token\",\"email\":\"owner@example.com\",\"name\":\"Olga\",\"password\":\"$pw\"}" \
  "$base/api/v1/setup/first-user" >/dev/null || fail "the first user was not created"
csrf=$(tr -d '\r' < "$hdr" | awk -F': ' 'tolower($1)=="x-csrf-token"{print $2}')
[ -n "$csrf" ] || fail "no CSRF token in the answer"
api() { curl -fsS -b "$jar" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' "$@"; }
api -d '{"name":"Upgrade test"}' "$base/api/v1/projects" >/dev/null || fail "the project was not created"
token=$(api -d '{"name":"upgrade","scopes":["read"]}' "$base/api/v1/tokens" | jq -r .token)
case "$token" in pp_*) ;; *) fail "no API token" ;; esac
projects_before=$(api "$base/api/v1/projects" | jq -S '[.items[] | {id, name, slug}]')
[ "$(printf '%s' "$projects_before" | jq length)" -ge 1 ] || fail "the project is not listed"
api "$base/api/v1/audit/verify" | jq -e '.ok == true' >/dev/null || fail "the audit chain is broken before the upgrade"
docker stop "$old" >/dev/null

# ---- The new version on the same database ----
say "Starting the new server on the same database"
start_server "$new" "$work/new/bin"
migrations_new=$(psql "SELECT count(*) FROM schema_migrations")
[ "$migrations_new" -ge "$migrations_old" ] || fail "the new version has fewer migrations ($migrations_new) than the old one ($migrations_old)"
curl -fsS "$base/healthz" | jq -e '.status == "ok"' >/dev/null || fail "/healthz is not ok after the upgrade"
api "$base/api/v1/me" | jq -e '.user.email == "owner@example.com"' >/dev/null || fail "the old session does not work after the upgrade"
curl -fsS -H 'Content-Type: application/json' -d "{\"email\":\"owner@example.com\",\"password\":\"$pw\"}" \
  "$base/api/v1/auth/login" >/dev/null || fail "the password login does not work after the upgrade"
projects_after=$(curl -fsS -H "Authorization: Bearer $token" "$base/api/v1/projects" | jq -S '[.items[] | {id, name, slug}]') \
  || fail "the API token does not work after the upgrade"
[ "$projects_before" = "$projects_after" ] || fail "the projects changed in the upgrade"
api "$base/api/v1/audit/verify" | jq -e '.ok == true' >/dev/null || fail "the audit chain is broken after the upgrade"
version=$(curl -fsS "$base/api/v1/version" | jq -r .commit)

say "PASS: upgrade from $from to $to (server commit $version). Migrations: $migrations_old -> $migrations_new."
