#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Acceptance tests of the Terraform provider against a live polipan-server (T-E2E-12, AC-TF-01, AC-TF-02).
# Needs: docker, a PostgreSQL on the host (default: 127.0.0.1:55432, user postgres, password pp, run in
# the container "polipan-testdb"), and internet access once, to get the terraform binary.
# It builds the server from the committed HEAD in docker, makes a fresh database, starts the server,
# sets up an owner and an API token, runs "go test" with TF_ACC=1, then removes everything it made.
# Env: PP_PG_CONTAINER, PP_PG_PORT, PP_PG_PASSWORD, TERRAFORM_VERSION, TERRAFORM_BIN (path to a terraform binary, skips the download).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PG_CONTAINER="${PP_PG_CONTAINER:-polipan-testdb}"
PG_PORT="${PP_PG_PORT:-55432}"
PG_PASS="${PP_PG_PASSWORD:-pp}"
TF_VERSION="${TERRAFORM_VERSION:-1.9.8}"
GO_IMAGE="golang:1.26"
SUFFIX="$$"
DB="pp_tfacc_${SUFFIX}"
SERVER_NAME="pp-tfacc-server-${SUFFIX}"
WORK="$(mktemp -d /tmp/pp-tfacc.XXXXXX)"
SRC="$WORK/src"; OUT="$WORK/out"; TFDIR="$WORK/tf"; DL="$WORK/dl"
mkdir -p "$SRC" "$OUT" "$TFDIR" "$DL"

free_kb() { df --output=avail -k / | tail -1 | tr -d ' '; }
show_disk() { echo "disk free on /: $(df -h --output=avail / | tail -1 | tr -d ' ')"; }
START_KB="$(free_kb)"
[ "$START_KB" -gt 5000000 ] || { echo "less than 5 GB free, stop"; exit 1; }
show_disk

psql_pg() { docker exec -e PGPASSWORD="$PG_PASS" "$PG_CONTAINER" psql -h 127.0.0.1 -p "$PG_PORT" -U postgres -v ON_ERROR_STOP=1 "$@"; }

cleanup() {
  rc=$?
  set +e
  docker logs "$SERVER_NAME" > "$WORK/server.log" 2>&1
  [ "$rc" -eq 0 ] || { echo "--- server log (tail) ---"; tail -n 30 "$WORK/server.log"; }
  docker rm -f "$SERVER_NAME" >/dev/null 2>&1
  psql_pg -c "DROP DATABASE IF EXISTS \"$DB\" WITH (FORCE)" >/dev/null 2>&1
  # Files written as root by docker are removed through docker.
  docker run --rm -v "$WORK:/w" alpine:latest sh -c 'rm -rf /w/* /w/.[!.]*' >/dev/null 2>&1
  rm -rf "$WORK"
  END_KB="$(free_kb)"
  echo "cleanup done: container, database $DB and $WORK removed"
  echo "disk free change: $(( (END_KB - START_KB) / 1024 )) MB (positive = more free than at start)"
  show_disk
  echo "note: the shared go caches (docker volumes ppgocache and ppgomod) are kept on purpose"
  exit "$rc"
}
trap cleanup EXIT

# 1. terraform binary
if [ -n "${TERRAFORM_BIN:-}" ]; then
  cp "$TERRAFORM_BIN" "$TFDIR/terraform"
else
  echo "== download terraform $TF_VERSION"
  docker run --rm --user "$(id -u):$(id -g)" -v "$DL:/dl" -v "$TFDIR:/tf" alpine:latest sh -c \
    "wget -q -O /dl/tf.zip https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip && unzip -q -o /dl/tf.zip -d /tf" \
    || { echo "cannot download terraform: no internet access? Set TERRAFORM_BIN to a local binary."; exit 2; }
fi
chmod 0755 "$TFDIR/terraform"

# 2. server binary from the committed HEAD (not from the working tree of other people)
echo "== build server"
git -C "$ROOT" archive HEAD | tar -x -C "$SRC"
docker run --rm -e GOTOOLCHAIN=local -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
  -v "$SRC:/src" -v "$OUT:/out" -v ppgocache:/root/.cache/go-build -v ppgomod:/go/pkg/mod -w /src "$GO_IMAGE" \
  sh -c "go build -trimpath -o /out/polipan-server ./server/cmd/polipan-server && chown -R $(id -u):$(id -g) /out"

# 3. fresh database
echo "== create database $DB"
psql_pg -c "CREATE DATABASE \"$DB\"" >/dev/null

# 4. free ports
free_port() { for p in $(shuf -i 20000-29999 -n 50); do ss -ltn | grep -q ":$p " || { echo "$p"; return; }; done; return 1; }
PORT="$(free_port)"; GRPC_PORT="$(free_port)"
head -c 32 /dev/urandom | base64 > "$WORK/master.key"; chmod 0400 "$WORK/master.key"

SERVER_ENV=(-e "POLIPAN_DATABASE_URL=postgres://postgres:${PG_PASS}@127.0.0.1:${PG_PORT}/${DB}?sslmode=disable"
  -e "POLIPAN_LISTEN=127.0.0.1:${PORT}" -e "POLIPAN_AGENT_LISTEN=127.0.0.1:${GRPC_PORT}"
  -e "POLIPAN_EXTERNAL_URL=http://127.0.0.1:${PORT}" -e "POLIPAN_AGENT_HOSTS=127.0.0.1,localhost"
  -e POLIPAN_MASTER_KEY_FILE=/work/master.key -e POLIPAN_API_RATE_LIMIT=100000)

echo "== start server on port $PORT"
docker run -d --name "$SERVER_NAME" --user "$(id -u):$(id -g)" --network host "${SERVER_ENV[@]}" \
  -v "$OUT/polipan-server:/polipan-server:ro" -v "$WORK/master.key:/work/master.key:ro" alpine:latest /polipan-server >/dev/null
URL="http://127.0.0.1:${PORT}"
for i in $(seq 1 60); do
  curl -fs "$URL/api/v1/healthz" >/dev/null 2>&1 && break
  [ "$i" -lt 60 ] || { echo "server did not start"; exit 1; }
  sleep 1
done

# 5. owner and API token
echo "== set up owner and token"
SETUP_URL="$(docker run --rm --user "$(id -u):$(id -g)" --network host "${SERVER_ENV[@]}" -v "$OUT/polipan-server:/polipan-server:ro" -v "$WORK/master.key:/work/master.key:ro" alpine:latest /polipan-server setup-url)"
SETUP_TOKEN="${SETUP_URL##*token=}"
PASS="tfacc-Password-12345"
curl -fs -D "$WORK/h1" -c "$WORK/jar" -H 'Content-Type: application/json' \
  -d "{\"setup_token\":\"$SETUP_TOKEN\",\"email\":\"owner@example.com\",\"name\":\"Owner\",\"password\":\"$PASS\"}" \
  "$URL/api/v1/setup/first-user" >/dev/null
CSRF="$(grep -i '^x-csrf-token:' "$WORK/h1" | awk '{print $2}' | tr -d '\r')"
TOKEN="$(curl -fs -b "$WORK/jar" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"tf-acc","scopes":["admin"]}' "$URL/api/v1/tokens" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || { echo "no token"; exit 1; }

# 6. tests
echo "== run acceptance tests"
docker run --rm --network host -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false \
  -e TF_ACC=1 -e "POLIPAN_URL=$URL" -e "POLIPAN_TOKEN=$TOKEN" -e TF_ACC_TERRAFORM_PATH=/tf/terraform -e TF_IN_AUTOMATION=1 \
  -v "$ROOT:/src" -v "$TFDIR:/tf:ro" -v ppgocache:/root/.cache/go-build -v ppgomod:/go/pkg/mod -w /src/terraform-provider "$GO_IMAGE" \
  sh -c 'go test ./internal/provider/ -run TestAcc -v -count=1 -timeout 20m; rc=$?; chown -R '"$(id -u):$(id -g)"' /src/terraform-provider; exit $rc'
