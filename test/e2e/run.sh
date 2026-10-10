#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# End-to-end test of milestones M1 and M2 (T-E2E-01, T-E2E-02 without rollback, T-E2E-13, AC-AUTH-03, AC-DOM-01..05, AC-APP-03, AC-APP-05, AC-NODE-02).
# It needs Docker on the host. Everything runs in a throw-away Docker host (docker:dind), so the
# real Docker of the machine is not touched. Certificates come from a local ACME CA (Pebble).
#
#   sh test/e2e/run.sh              builds the images and runs the whole test
#   OUTAGE_SECONDS=60 sh ...        shorter server outage (the done criterion uses 300)
#   KEEP=1 sh ...                   keep the test Docker host after the run
#   E2E_HOST=name sh ...            name of the test Docker host (default pp-e2e-dind)
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
host=${E2E_HOST:-pp-e2e-dind}
outage=${OUTAGE_SECONDS:-300}
dx() { docker exec "$host" "$@"; }
step() { printf '\n== %s\n' "$*"; }

cleanup() {
  [ -n "${KEEP:-}" ] || docker rm -f "$host" >/dev/null 2>&1 || true
}
trap cleanup EXIT

step "Build the images"
make images >/dev/null

step "Start the test Docker host"
docker rm -f "$host" >/dev/null 2>&1 || true
docker run -d --privileged --name "$host" -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
i=0
until dx docker version >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the test Docker host did not start"; exit 1; }
  sleep 1
done
for img in polipan-server:dev polipan-agent:dev python:3.12-slim postgres:16-alpine; do
  docker pull -q "$img" >/dev/null 2>&1 || true
  docker save "$img" | docker exec -i "$host" docker load >/dev/null
done
dx sh -c 'for i in caddy:2.10.2 nginx:alpine ghcr.io/letsencrypt/pebble:latest ghcr.io/letsencrypt/pebble-challtestsrv:latest; do docker pull -q $i >/dev/null; done'
dx mkdir -p /e2e
docker cp test/e2e/. "$host:/e2e/"
docker cp deploy/compose/docker-compose.yml "$host:/e2e/docker-compose.yml"

step "Install the control plane and the agent"
dx sh /e2e/setup.sh

driver() { dx docker run --rm --net host -v /e2e:/e2e python:3.12-slim python -I /e2e/driver.py "$@"; }

step "T-E2E-01: setup, login, image service, domain, HTTPS, stop and start"
driver flow

step "T-E2E-02: Git app deploys on push (fake Git server, rollback part is M3)"
# The fake Git server is on its own network (RFC 1918, not a Docker network of Polipan), so the
# org setting allow_private_targets lets the server clone from it (SSRF deny list, REQ-SEC-104).
dx docker network create --subnet 192.168.77.0/24 gitnet >/dev/null
dx docker run -d --name gitserver --network gitnet --ip 192.168.77.10 -v /e2e/gitapp:/app python:3.12-slim \
  sh -c 'apt-get update -qq && apt-get install -y -qq git >/dev/null && python3 -I /app/gitserver.py 8000 /srv/git' >/dev/null
dx docker network connect gitnet "$(dx docker compose -f /opt/polipan/docker-compose.yml ps -q server)"
dx docker compose -f /opt/polipan/docker-compose.yml exec -T postgres psql -q -U polipan -d polipan \
  -c "UPDATE orgs SET settings = settings || '{\"allow_private_targets\": true}'::jsonb"
i=0
until dx docker run --rm --network gitnet python:3.12-slim python3 -c 'import urllib.request; urllib.request.urlopen("http://192.168.77.10:8000/app.git/info/refs?service=git-upload-pack", timeout=3)' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 90 ] || { dx docker logs gitserver; exit 1; }
  sleep 2
done
driver gitflow

step "AC-NODE-02: a used join token is refused"
if dx docker run --rm --net host -v /var/run/docker.sock:/var/run/docker.sock -v /e2e:/e2e \
  polipan-agent:dev join --server 127.0.0.1:9443 --token "$(dx cat /e2e/join-token)" \
  --ca-fingerprint "$(dx docker compose -f /opt/polipan/docker-compose.yml exec -T server /polipan-server ca-fingerprint)" \
  --data-dir /tmp/second-agent; then
  echo "FAIL: a used token was accepted"; exit 1
fi
echo "ok: used token refused"

step "T-E2E-13: server stopped for $outage s, the app keeps serving"
dx docker compose -f /opt/polipan/docker-compose.yml stop server
# The agent must also restart a dead app container while the server is down.
dx sh -c 'docker kill $(docker ps -q --filter label=polipan.placement)' >/dev/null
end=$(( $(date +%s) + outage ))
driver serving
while [ "$(date +%s)" -lt "$end" ]; do
  sleep 30
  driver serving
done
restarted=$(dx date +%s)
dx docker compose -f /opt/polipan/docker-compose.yml start server
i=0
until dx docker compose -f /opt/polipan/docker-compose.yml exec -T server /polipan-server healthcheck >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "the server did not come back"; exit 1; }
  sleep 2
done
# The agent reconnects with back-off (max 30 s) and the node becomes ready again.
i=0
until driver after "$restarted" >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { driver after "$restarted"; exit 1; }
  sleep 5
done
driver after "$restarted"
echo
echo "E2E PASSED"
