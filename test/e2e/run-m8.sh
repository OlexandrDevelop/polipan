#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# End-to-end test of milestone M8 (T-E2E-04, T-E2E-09 alert part, T-E2E-15, AC-OBS-01, 03, 04 to 07, AC-ALERT-01, 03 to 05,
# AC-DEPLOY-03, REQ-OBS-102, REQ-OBS-104). One node is one Docker-in-Docker container (docker:dind) with the control plane
# (PostgreSQL, VictoriaMetrics, VictoriaLogs, the server), a fake alert receiver and the agent (which starts vmagent).
# Nothing of the real Docker of the machine is changed, apart from the network and the images named below.
# They are removed at the end, with all the test data of VictoriaMetrics and VictoriaLogs.
#
#   sh test/e2e/run-m8.sh            builds the images, runs the test and cleans up
#   KEEP=1 sh ...                    keep the test container after the run
#   SKIP_BUILD=1 sh ...              use the images polipan-server:m8 and polipan-agent:m8 that exist
#   STEPS="setup canary" sh ...      run only these steps of the driver (default: all)
#
# Disk: it needs about 4 GB. It stops when less than 5 GB is free.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
net=pp-m8
n1=pp-m8-n1
ip1=172.29.8.11
steps=${STEPS:-"setup canary logs search alerts"}
step() { printf '\n== %s\n' "$*"; }
free_gb() { df -BG --output=avail / | tail -1 | tr -dc '0-9'; }
dx() { docker exec "$n1" "$@"; }
driver() { python3 -I "$root/test/e2e/driver_m8.py" "$@"; }

start_free=$(free_gb)
[ "$start_free" -ge 5 ] || { echo "only $start_free GB free: stop"; exit 1; }

cleanup() {
  code=$?
  if [ "$code" -ne 0 ]; then
    echo "--- server log (last lines) ---"
    docker exec "$n1" docker logs --tail 60 server 2>&1 || true
    echo "--- agent log (last lines) ---"
    docker exec "$n1" docker logs --tail 40 agent 2>&1 || true
  fi
  if [ -z "${KEEP:-}" ]; then
    docker rm -fv "$n1" >/dev/null 2>&1 || true
    docker network rm "$net" >/dev/null 2>&1 || true
    [ -n "${SKIP_BUILD:-}" ] || docker image rm polipan-server:m8 polipan-agent:m8 >/dev/null 2>&1 || true
  fi
  end_free=$(free_gb)
  echo "disk: $start_free GB free at the start, $end_free GB free now"
}
trap cleanup EXIT

if [ -z "${SKIP_BUILD:-}" ]; then
  step "Build the server and the agent"
  out=$(mktemp -d)
  docker run --rm -v "$root":/src -v pp-gocache:/root/.cache/go-build -v pp-gomod:/go/pkg/mod -v "$out":/out -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.23 \
    sh -c 'go build -trimpath -ldflags "-s -w" -o /out/polipan-server ./server/cmd/polipan-server && go build -trimpath -ldflags "-s -w" -o /out/polipan-agent ./agent/cmd/polipan-agent && chown -R '"$(id -u):$(id -g)"' /out'
  mkdir -p "$out/server" "$out/agent"
  cp "$out/polipan-server" "$out/server/" && cp test/e2e/m7/server.Dockerfile "$out/server/Dockerfile"
  cp "$out/polipan-agent" "$out/agent/" && cp test/e2e/m8/agent.Dockerfile "$out/agent/Dockerfile"
  docker build -q -t polipan-server:m8 "$out/server" >/dev/null
  docker build -q -t polipan-agent:m8 "$out/agent" >/dev/null
  rm -rf "$out"
fi

step "Start the test node"
docker rm -fv "$n1" >/dev/null 2>&1 || true
docker network rm "$net" >/dev/null 2>&1 || true
docker network create --subnet 172.29.8.0/24 "$net" >/dev/null
docker run -d --privileged --name "$n1" --hostname "$n1" --network "$net" --ip "$ip1" -e DOCKER_TLS_CERTDIR= docker:27-dind >/dev/null
i=0
until dx docker version >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "Docker did not start"; exit 1; }
  sleep 1
done

step "Load the images"
docker save polipan-agent:m8 | docker exec -i "$n1" docker load >/dev/null
docker save polipan-server:m8 | docker exec -i "$n1" docker load >/dev/null
for img in caddy:2.10.2 alpine:3.20 python:3.12-slim postgres:16-alpine victoriametrics/victoria-metrics:v1.108.1 \
  victoriametrics/victoria-logs:v1.9.1-victorialogs victoriametrics/vmagent:v1.108.1; do
  docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null
  docker save "$img" | docker exec -i "$n1" docker load >/dev/null
done
dx mkdir -p /e2e
docker cp test/e2e/m8/. "$n1:/e2e/"

step "Control plane"
dx sh -c 'mkdir -p /etc/polipan /var/lib/polipan-agent && head -c 32 /dev/urandom | base64 > /etc/polipan/master.key && chown 65532:65532 /etc/polipan/master.key && chmod 0400 /etc/polipan/master.key'
dx docker network create --subnet 10.210.1.0/24 --ip-range 10.210.1.128/25 --gateway 10.210.1.1 polipan >/dev/null
dx docker network create --subnet 172.31.251.0/24 cp >/dev/null
dx docker network create --subnet 192.168.78.0/24 rcv >/dev/null
dx docker run -d --name pg --network cp --network-alias pg -e POSTGRES_USER=polipan -e POSTGRES_DB=polipan -e POSTGRES_PASSWORD=pw postgres:16-alpine >/dev/null
dx docker run -d --name vm --network cp -v vm-data:/storage victoriametrics/victoria-metrics:v1.108.1 \
  -storageDataPath=/storage -retentionPeriod=365d -httpAuth.username=polipan -httpAuth.password=vpw >/dev/null
dx docker run -d --name vl --network cp -v vl-data:/storage victoriametrics/victoria-logs:v1.9.1-victorialogs \
  -storageDataPath=/storage -retentionPeriod=90d -httpAuth.username=polipan -httpAuth.password=vpw >/dev/null
dx docker run -d --name rcv --network rcv --ip 192.168.78.10 -v /e2e:/e2e python:3.12-slim python -u /e2e/receiver.py 8081 >/dev/null
i=0
until dx docker exec pg pg_isready -U polipan -d polipan >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "PostgreSQL did not start"; exit 1; }
  sleep 1
done
dx docker run -d --name server --network cp --restart unless-stopped -p 9443:9443 -p 9444:9444 \
  -v /etc/polipan/master.key:/etc/polipan/master.key:ro \
  -e POLIPAN_DATABASE_URL='postgres://polipan:pw@pg:5432/polipan?sslmode=disable' -e POLIPAN_LISTEN=:8080 \
  -e POLIPAN_EXTERNAL_URL=http://panel.test -e POLIPAN_AGENT_HOSTS="$ip1,localhost" -e POLIPAN_MASTER_KEY_FILE=/etc/polipan/master.key \
  -e POLIPAN_NODE_SUSPECT_AFTER=10s -e POLIPAN_NODE_DOWN_AFTER=30s \
  -e POLIPAN_VM_URL=http://vm:8428 -e POLIPAN_VL_URL=http://vl:9428 -e POLIPAN_VICTORIA_USER=polipan -e POLIPAN_VICTORIA_PASSWORD=vpw \
  -e POLIPAN_TELEGRAM_API=http://192.168.78.10:8081 -e POLIPAN_VMAGENT_IMAGE=victoriametrics/vmagent:v1.108.1 \
  polipan-server:m8 >/dev/null
dx docker network connect --alias polipan-server --ip 10.210.1.250 polipan server
dx docker network connect rcv server
i=0
until dx docker exec server /polipan-server healthcheck >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { dx docker logs server; exit 1; }
  sleep 2
done
fp=$(dx docker exec server /polipan-server ca-fingerprint)
out=$(dx docker exec server /polipan-server join-token --name n1 --label ingress=true)
token=$(printf '%s\n' "$out" | sed -n 's/^token=//p')
dx docker run --rm --net host -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/polipan-agent:/var/lib/polipan-agent \
  polipan-agent:m8 join --server 127.0.0.1:9443 --token "$token" --ca-fingerprint "$fp" --data-dir /var/lib/polipan-agent
dx docker run -d --name agent --restart always --net host --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock -v /var/lib/polipan-agent:/var/lib/polipan-agent \
  -v /var/lib/docker/volumes:/var/lib/docker/volumes \
  -e POLIPAN_AGENT_SERVER=127.0.0.1:9443 -e POLIPAN_AGENT_FIREWALL=false polipan-agent:m8 >/dev/null
dx docker exec server /polipan-server setup-url > /tmp/pp-m8-setup-url

for s in $steps; do
  step "driver: $s"
  driver "$s" "$n1" "$ip1" /tmp/pp-m8-setup-url
done

echo
echo "E2E M8 PASSED"
