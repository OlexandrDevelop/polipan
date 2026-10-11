#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# End-to-end test of milestone M9 (T-E2E-05, T-E2E-06, AC-PROMO-01, 02, 04, AC-PREV-01 to 04, AC-VOL-04).
# It needs Docker on the host. Everything runs in a throw-away Docker host (docker:dind), so the real Docker of the
# machine is not touched, apart from the two images that are built (polipan-server:dev and polipan-agent:dev).
# Certificates come from a local ACME CA (Pebble). The Git repository and the Git API (statuses, comments) are a
# fake Git server (gitapp/gitserver.py).
#
#   sh test/e2e/run-m9.sh           builds the images, runs the test and cleans up
#   KEEP=1 sh ...                   keep the test Docker host after the run
#   SKIP_BUILD=1 sh ...             use the images polipan-server:dev and polipan-agent:dev that exist
#   STEPS="setup promote" sh ...    run only these steps of the driver (default: setup promote preview)
#
# Disk: it needs about 6 GB. It stops when less than 5 GB is free. It removes the test host and the images it built.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
host=${E2E_HOST:-pp-e2e-m9}
steps=${STEPS:-"setup promote preview"}
dx() { docker exec "$host" "$@"; }
step() { printf '\n== %s\n' "$*"; }
free_gb() { df -BG --output=avail / | tail -1 | tr -dc '0-9'; }

start_free=$(free_gb)
[ "$start_free" -ge 5 ] || { echo "only $start_free GB free: stop"; exit 1; }

cleanup() {
  code=$?
  if [ "$code" -ne 0 ]; then
    echo "--- server log (last lines) ---"
    docker exec "$host" docker compose -f /opt/polipan/docker-compose.yml logs --tail 60 server 2>&1 || true
    echo "--- agent log (last lines) ---"
    docker exec "$host" docker logs --tail 40 agent 2>&1 || true
  fi
  if [ -z "${KEEP:-}" ]; then
    docker rm -fv "$host" >/dev/null 2>&1 || true
    [ -n "${SKIP_BUILD:-}" ] || docker image rm polipan-server:dev polipan-agent:dev >/dev/null 2>&1 || true
  fi
  end_free=$(free_gb)
  echo "disk: $start_free GB free at the start, $end_free GB free now"
}
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  step "Build the server and the agent image"
  docker build -q -f deploy/docker/server.Dockerfile --build-arg VERSION=e2e --build-arg COMMIT=e2e -t polipan-server:dev . >/dev/null
  docker build -q -f deploy/docker/agent.Dockerfile --build-arg VERSION=e2e --build-arg COMMIT=e2e -t polipan-agent:dev . >/dev/null
fi

step "Start the test Docker host"
docker rm -fv "$host" >/dev/null 2>&1 || true
docker run -d --privileged --name "$host" -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
i=0
until dx docker version >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the test Docker host did not start"; exit 1; }
  sleep 1
done
for img in polipan-server:dev polipan-agent:dev python:3.12-slim postgres:16-alpine; do
  docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null
  docker save "$img" | docker exec -i "$host" docker load >/dev/null
done
dx sh -c 'for i in caddy:2.10.2 nginx:alpine ghcr.io/letsencrypt/pebble:latest ghcr.io/letsencrypt/pebble-challtestsrv:latest; do docker pull -q $i >/dev/null; done'
dx mkdir -p /e2e
docker cp test/e2e/. "$host:/e2e/"
docker cp deploy/compose/docker-compose.yml "$host:/e2e/docker-compose.yml"

step "Install the control plane and the agent"
dx sh /e2e/setup.sh

step "Start the fake Git server"
dx docker network create --subnet 192.168.77.0/24 gitnet >/dev/null
dx docker run -d --name gitserver --network gitnet --ip 192.168.77.10 -v /e2e/gitapp:/app python:3.12-slim \
  sh -c 'apt-get update -qq && apt-get install -y -qq git >/dev/null && python3 -I /app/gitserver.py 8000 /srv/git' >/dev/null
dx docker network connect gitnet "$(dx docker compose -f /opt/polipan/docker-compose.yml ps -q server)"
i=0
until dx docker run --rm --network gitnet python:3.12-slim python3 -c 'import urllib.request; urllib.request.urlopen("http://192.168.77.10:8000/app.git/info/refs?service=git-upload-pack", timeout=3)' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 90 ] || { dx docker logs gitserver; exit 1; }
  sleep 2
done

driver() {
  dx docker run --rm --net host -v /e2e:/e2e -v /var/run/docker.sock:/var/run/docker.sock -v /usr/local/bin/docker:/usr/local/bin/docker:ro \
    python:3.12-slim python -I /e2e/driver_m9.py "$@"
}
for s in $steps; do
  step "driver: $s"
  driver "$s"
done

echo
echo "E2E M9 PASSED"
