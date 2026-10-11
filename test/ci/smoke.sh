#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Gate 11: the server image starts against PostgreSQL and /healthz answers ok.
# The agent and cli images answer "version". Usage: smoke.sh <server-image> <agent-image> <cli-image>
set -eu

server_image=$1
agent_image=$2
cli_image=$3
suffix=$$
net="pp-smoke-$suffix"
pg="pp-smoke-pg-$suffix"
srv="pp-smoke-server-$suffix"

cleanup() {
  docker rm -fv "$srv" "$pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --rm "$agent_image" version
docker run --rm "$cli_image" version

docker network create "$net" >/dev/null
docker run -d --name "$pg" --network "$net" -e POSTGRES_PASSWORD=pw postgres:16 >/dev/null
i=0
until docker exec "$pg" pg_isready -U postgres >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo "PostgreSQL did not start"; exit 1; }
  sleep 1
done

# The server runs as a normal user and cannot write /etc/polipan, so it gets the master key in the environment.
master_key=$(head -c 32 /dev/urandom | base64)
docker run -d --name "$srv" --network "$net" \
  -e "POLIPAN_DATABASE_URL=postgres://postgres:pw@$pg:5432/postgres?sslmode=disable" \
  -e "POLIPAN_MASTER_KEY=$master_key" \
  "$server_image" >/dev/null
i=0
until docker exec "$srv" /polipan-server healthcheck >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then
    echo "server did not become healthy"
    docker logs "$srv"
    exit 1
  fi
  sleep 1
done
echo "server healthy"
docker run --rm --network "$net" "$cli_image" --server "http://$srv:8080" version
