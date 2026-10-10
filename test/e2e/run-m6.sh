#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# T-E2E-12: config as code in a real stack (AC-CAC-01 to AC-CAC-06, AC-CLI-01 to 02 in part, AC-TF-01, AC-TF-02).
# A real polipan-server with PostgreSQL runs in Docker. The real CLI binary plans and applies a file, a hand edit is
# refused, prune rules are checked, the export has no secret values. At the end the Terraform provider tests run
# against another fresh server (terraform-provider/test/acc.sh).
# There is no node: a deployment that needs a node stays queued. Config sync of a push (webhook) is tested in
# server/internal/api/m6_test.go with a fake Git clone, because it needs a Git server.
#
# Needs: docker, the PostgreSQL container polipan-testdb (see terraform-provider/test/acc.sh), internet once for terraform.
# Disk: stops when less than 5 GB is free. Everything it makes is removed at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PG_CONTAINER="${PP_PG_CONTAINER:-polipan-testdb}"
PG_PORT="${PP_PG_PORT:-55432}"
PG_PASS="${PP_PG_PASSWORD:-pp}"
GO_IMAGE="golang:1.23"
SUFFIX="$$"
DB="pp_e2e12_${SUFFIX}"
SERVER_NAME="pp-e2e12-server-${SUFFIX}"
WORK="$(mktemp -d /tmp/pp-e2e12.XXXXXX)"
SRC="$WORK/src"; OUT="$WORK/out"
mkdir -p "$SRC" "$OUT" "$WORK/home"

free_kb() { df --output=avail -k / | tail -1 | tr -d ' '; }
START_KB="$(free_kb)"
[ "$START_KB" -gt 5000000 ] || { echo "less than 5 GB free, stop"; exit 1; }
step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*"; exit 1; }
psql_pg() { docker exec -e PGPASSWORD="$PG_PASS" "$PG_CONTAINER" psql -h 127.0.0.1 -p "$PG_PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }

cleanup() {
  rc=$?
  set +e
  [ "$rc" -eq 0 ] || { echo "--- server log (tail) ---"; docker logs "$SERVER_NAME" 2>&1 | tail -n 25; }
  docker rm -f "$SERVER_NAME" >/dev/null 2>&1
  psql_pg -c "DROP DATABASE IF EXISTS \"$DB\" WITH (FORCE)" >/dev/null 2>&1
  docker run --rm -v "$WORK:/w" alpine:latest sh -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1
  rm -rf "$WORK"
  END_KB="$(free_kb)"
  echo "cleanup done: container, database $DB and $WORK removed"
  echo "disk free change: $(( (END_KB - START_KB) / 1024 )) MB, free now: $(df -h --output=avail / | tail -1 | tr -d ' ')"
  exit "$rc"
}
trap cleanup EXIT

step "build the server and the CLI from the committed HEAD"
git -C "$ROOT" archive HEAD | tar -x -C "$SRC"
docker run --rm -e GOTOOLCHAIN=local -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
  -v "$SRC:/src" -v "$OUT:/out" -v ppgocache:/root/.cache/go-build -v ppgomod:/go/pkg/mod -w /src "$GO_IMAGE" \
  sh -c "go build -trimpath -o /out/polipan-server ./server/cmd/polipan-server && go build -trimpath -o /out/polipan ./cli/cmd/polipan && chown -R $(id -u):$(id -g) /out"
CLI="$OUT/polipan"

step "start the server"
psql_pg -c "CREATE DATABASE \"$DB\"" >/dev/null
free_port() { for p in $(shuf -i 20000-29999 -n 50); do ss -ltn | grep -q ":$p " || { echo "$p"; return; }; done; return 1; }
PORT="$(free_port)"; GRPC_PORT="$(free_port)"
head -c 32 /dev/urandom | base64 > "$WORK/master.key"; chmod 0400 "$WORK/master.key"
SERVER_ENV=(-e "POLIPAN_DATABASE_URL=postgres://postgres:${PG_PASS}@127.0.0.1:${PG_PORT}/${DB}?sslmode=disable"
  -e "POLIPAN_LISTEN=127.0.0.1:${PORT}" -e "POLIPAN_AGENT_LISTEN=127.0.0.1:${GRPC_PORT}"
  -e "POLIPAN_EXTERNAL_URL=http://127.0.0.1:${PORT}" -e "POLIPAN_AGENT_HOSTS=127.0.0.1,localhost"
  -e POLIPAN_MASTER_KEY_FILE=/work/master.key -e POLIPAN_API_RATE_LIMIT=100000)
docker run -d --name "$SERVER_NAME" --user "$(id -u):$(id -g)" --network host "${SERVER_ENV[@]}" \
  -v "$OUT/polipan-server:/polipan-server:ro" -v "$WORK/master.key:/work/master.key:ro" alpine:latest /polipan-server >/dev/null
URL="http://127.0.0.1:${PORT}"
for i in $(seq 1 60); do
  curl -fs "$URL/api/v1/healthz" >/dev/null 2>&1 && break
  [ "$i" -lt 60 ] || fail "the server did not start"
  sleep 1
done
SETUP_URL="$(docker run --rm --user "$(id -u):$(id -g)" --network host "${SERVER_ENV[@]}" -v "$OUT/polipan-server:/polipan-server:ro" -v "$WORK/master.key:/work/master.key:ro" alpine:latest /polipan-server setup-url)"
SETUP_TOKEN="${SETUP_URL##*token=}"
curl -fs -D "$WORK/h1" -c "$WORK/jar" -H 'Content-Type: application/json' \
  -d "{\"setup_token\":\"$SETUP_TOKEN\",\"email\":\"owner@example.com\",\"name\":\"Owner\",\"password\":\"e2e-Password-12345\"}" \
  "$URL/api/v1/setup/first-user" >/dev/null
CSRF="$(grep -i '^x-csrf-token:' "$WORK/h1" | awk '{print $2}' | tr -d '\r')"
TOKEN="$(curl -fs -b "$WORK/jar" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"e2e","scopes":["admin"]}' "$URL/api/v1/tokens" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "no token"

export POLIPAN_URL="$URL" POLIPAN_TOKEN="$TOKEN" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/home/.config" POLIPAN_PROJECT=blog
api() { curl -s -o "$WORK/body" -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"; }
jget() { python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$WORK/body" "$1"; }

step "project with one environment"
"$CLI" project create blog >/dev/null
"$CLI" env rm staging --yes >/dev/null 2>&1 || true

cat > "$WORK/polipan.yaml" <<'EOF'
version: 1
project: blog
services:
  web:
    source:
      image: { name: "nginx:1.27" }
    ports: [80]
    env:
      LOG_LEVEL: info
    domains:
      - host: blog.example.com
        port: 80
  worker:
    source:
      image: { name: "busybox:1.36" }
    command: ["sleep", "3600"]
EOF
cat > "$WORK/bad.yaml" <<'EOF'
version: 1
project: blog
services:
  web:
    source:
      image: { name: "nginx:1.27" }
    not_a_real_key: true
EOF

step "validate offline: an unknown key gives line and field (AC-CAC-05)"
"$CLI" validate -f "$WORK/polipan.yaml" >/dev/null || fail "a good file does not validate"
if "$CLI" validate -f "$WORK/bad.yaml" > "$WORK/v.out" 2>&1; then fail "a bad file validates"; fi
grep -q 'not_a_real_key' "$WORK/v.out" || fail "validate does not name the field: $(cat "$WORK/v.out")"
grep -Eq '"line": ?7|:7' "$WORK/v.out" || fail "validate does not give the line: $(cat "$WORK/v.out")"
if "$CLI" plan -f "$WORK/bad.yaml" >/dev/null 2>&1; then fail "plan accepts a bad file"; fi

step "plan changes nothing (AC-CAC-01)"
"$CLI" plan -f "$WORK/polipan.yaml" > "$WORK/plan.out"
grep -q 'create' "$WORK/plan.out" || fail "plan shows no create: $(cat "$WORK/plan.out")"
N="$("$CLI" service ls --env production -o json | python3 -I -c 'import json,sys; print(len(json.load(sys.stdin)))')"
[ "$N" = 0 ] || fail "plan made $N services"

step "apply makes the services (AC-CAC-02)"
"$CLI" apply -f "$WORK/polipan.yaml" --yes >/dev/null || fail "apply failed"
"$CLI" service ls --env production -o json | python3 -I -c '
import json,sys
names=sorted(s["name"] for s in json.load(sys.stdin))
assert names==["web","worker"], names'
"$CLI" plan -f "$WORK/polipan.yaml" > "$WORK/plan2.out"
grep -Eq '^\s*[+~-] ' "$WORK/plan2.out" && fail "the second plan is not empty: $(cat "$WORK/plan2.out")"

step "a hand edit of a field from the file is refused (AC-CAC-03)"
SVC="$("$CLI" service ls --env production -o json | python3 -I -c 'import json,sys; print([s["id"] for s in json.load(sys.stdin) if s["name"]=="web"][0])')"
CODE="$(api -X PATCH "$URL/api/v1/services/$SVC" -d '{"config":{"image":"nginx:1.26","port":80}}')"
[ "$CODE" = 409 ] || fail "hand edit gave $CODE: $(cat "$WORK/body")"
grep -q managed_by_git "$WORK/body" || fail "the error is not managed_by_git"

step "a service that is not in the file is unmanaged and stays without prune (AC-CAC-04)"
sed '/^  worker:/,$d' "$WORK/polipan.yaml" > "$WORK/one.yaml"
"$CLI" plan -f "$WORK/one.yaml" | grep -q 'unmanaged' || fail "worker is not shown as unmanaged"
"$CLI" apply -f "$WORK/one.yaml" --yes >/dev/null || fail "apply without prune failed"
"$CLI" service ls --env production -o json | python3 -I -c '
import json,sys
assert "worker" in [s["name"] for s in json.load(sys.stdin)], "worker was deleted without prune"'
"$CLI" apply -f "$WORK/one.yaml" --prune --yes >/dev/null || fail "apply with prune failed"
"$CLI" service ls --env production -o json | python3 -I -c '
import json,sys
assert "worker" not in [s["name"] for s in json.load(sys.stdin)], "worker is still there after prune"'

step "export has secret names and no values (AC-CAC-06)"
printf '%s' 'super-secret-e2e-value-9f3a' | "$CLI" secret set API_TOKEN --stdin >/dev/null || fail "secret set failed"
"$CLI" export > "$WORK/export.yaml"
grep -q 'API_TOKEN' "$WORK/export.yaml" || fail "the export has no secret name"
grep -q 'super-secret-e2e-value-9f3a' "$WORK/export.yaml" && fail "the export has a secret value"
"$CLI" validate -f "$WORK/export.yaml" >/dev/null || fail "the export does not validate"

step "the Terraform provider against a live server (AC-TF-01, AC-TF-02)"
if ! bash "$ROOT/terraform-provider/test/acc.sh" > "$WORK/tf.log" 2>&1; then tail -n 30 "$WORK/tf.log"; fail "the Terraform acceptance tests failed"; fi
grep -E '^(--- |ok |disk free change)' "$WORK/tf.log"

echo
echo "T-E2E-12: PASS"
